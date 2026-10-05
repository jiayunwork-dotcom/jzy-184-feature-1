package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"agristation/internal/model"
)

// MaxHistoricalBatch 单次历年资料导入条数上限（可带多站多年）。
const MaxHistoricalBatch = 20000

// HistoricalInput 历年资料导入单条入参。year 为标注的年份，date 必须
// 属于该年（YYYY-MM-DD）。
type HistoricalInput struct {
	StationCode string  `json:"station_code"`
	Year        int     `json:"year"`
	Date        string  `json:"date"`
	TMax        float64 `json:"tmax"`
	TMin        float64 `json:"tmin"`
}

// HistoricalItemResult 单条历年资料导入结果。
type HistoricalItemResult struct {
	Index int  `json:"index"`
	OK    bool `json:"ok"`
	// Applied=true 表示该条所属的站-年整体生效；非法条为 false。
	// 同 (站,日) 在本批中重复出现时只留最后一条，其余合法条
	// OK=true 但 Applied=false（不报错，说明被批内去重）。
	Applied bool   `json:"applied"`
	Reason  string `json:"reason,omitempty"`
}

// HistoricalYearResult 单个“站-年”的整体结果。
type HistoricalYearResult struct {
	StationCode string `json:"station_code"`
	Year        int    `json:"year"`
	// Applied=true 表示该站该年已整体替换为本次内容。
	Applied bool `json:"applied"`
	// Replaced=true 表示该站该年原本已有资料、本次是整年替换。
	Replaced bool `json:"replaced"`
	Days     int  `json:"days"`
	// Reasons 该组被整体拒绝时的逐条原因（合法条因同组非法被连带不生效，
	// 也在此说明）。
	Reasons []string `json:"reasons,omitempty"`
}

// ImportHistoricalResult 批量导入结果。
type ImportHistoricalResult struct {
	Items []HistoricalItemResult `json:"items"`
	Years []HistoricalYearResult `json:"years"`
}

type histKey struct {
	station string
	year    int
}

type histParsed struct {
	d   model.Date
	err error
}

// ImportHistorical 按站点、年份导入整年的逐日最高最低气温，一次可带多站
// 多年。规则：
//
//   - 非法记录逐条报原因（最低>最高、越界、站点不存在、日期格式错、
//     日期不属于所标年份），同批其余站-年照常生效；
//   - 一站一年只能整体生效或整体不生效：某站-年组里只要有一条非法，
//     该组全部条目不写入（合法条也不生效），其他站-年不受影响；
//   - 同一站同一年再导一次：在同一事务内先删后写，整年换掉旧的；
//   - 同批内同一 (站,日) 多条合法记录：折叠为最后出现的一条，其余
//     合法条 OK=true、Applied=false（不报错）；
//   - 同站同年并发导入：复用站点 advisory lock 串行化，最后提交的事务
//     完整覆盖，最终内容完整等于其中一次；
//   - 导入不触发地块重算、不改单点预测与阶段事件；试走结果只在查询时
//     按最终数据计算，因此任何交错顺序都等价于“只拿最终数据从头算”。
func (s *Service[T]) ImportHistorical(ctx context.Context, in []HistoricalInput) (*ImportHistoricalResult, error) {
	if len(in) == 0 {
		return &ImportHistoricalResult{}, nil
	}
	if len(in) > MaxHistoricalBatch {
		return nil, fmt.Errorf("单批最多 %d 条，当前 %d 条", MaxHistoricalBatch, len(in))
	}

	parsed := make([]histParsed, len(in))
	// raw 按入参自带的 (站,年) 归组，非法条也归组（用于整组拒绝）。
	raw := map[histKey][]int{}
	stationsTouched := map[string]struct{}{}
	for i, item := range in {
		k := histKey{station: item.StationCode, year: item.Year}
		raw[k] = append(raw[k], i)
		stationsTouched[item.StationCode] = struct{}{}

		if item.Year < 1900 || item.Year > 2200 {
			parsed[i].err = fmt.Errorf("年份 %d 超出合理范围 [1900,2200]", item.Year)
			continue
		}
		d, err := time.Parse("2006-01-02", item.Date)
		if err != nil {
			parsed[i].err = fmt.Errorf("日期格式应为 YYYY-MM-DD：%q", item.Date)
			continue
		}
		d = d.UTC()
		if d.Year() != item.Year {
			parsed[i].err = fmt.Errorf("日期 %s 不属于所标的年份 %d", item.Date, item.Year)
			continue
		}
		if err := ValidateTemp(item.TMax, item.TMin); err != nil {
			parsed[i].err = err
			continue
		}
		parsed[i].d = d
	}

	locked := make([]string, 0, len(stationsTouched))
	for st := range stationsTouched {
		locked = append(locked, st)
	}
	sort.Strings(locked)

	keys := make([]histKey, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].station != keys[j].station {
			return keys[i].station < keys[j].station
		}
		return keys[i].year < keys[j].year
	})

	res := &ImportHistoricalResult{
		Items: make([]HistoricalItemResult, len(in)),
		Years: make([]HistoricalYearResult, 0, len(keys)),
	}
	for i := range in {
		res.Items[i] = HistoricalItemResult{Index: i}
	}

	err := s.store.Update(ctx, func(tx T) error {
		if err := lockStations(tx, locked); err != nil {
			return err
		}
		stationOK := map[string]bool{}
		for _, code := range locked {
			st, err := tx.GetStation(code)
			if err != nil {
				return err
			}
			stationOK[code] = st != nil
		}

		for _, k := range keys {
			idxs := raw[k]
			yr := HistoricalYearResult{StationCode: k.station, Year: k.year}

			// 汇总该组所有条目的合法性。
			bad := map[int]string{} // 非法条索引 -> 原因
			for _, i := range idxs {
				if parsed[i].err != nil {
					bad[i] = parsed[i].err.Error()
				}
			}
			yearSane := k.year >= 1900 && k.year <= 2200
			stationMissing := k.station == "" || !stationOK[k.station]

			if len(bad) > 0 || stationMissing || !yearSane {
				// 整组拒绝：逐条回报。非法条给自身原因；合法条说明被连带。
				for _, i := range idxs {
					if reason, isBad := bad[i]; isBad {
						res.Items[i] = HistoricalItemResult{
							Index: i, OK: false, Applied: false, Reason: reason}
						yr.Reasons = append(yr.Reasons,
							fmt.Sprintf("第 %d 条：%s", i, reason))
					} else {
						reason := ""
						switch {
						case stationMissing:
							reason = fmt.Sprintf("站点 %s 不存在，同站同年整年未生效", k.station)
						case !yearSane:
							reason = fmt.Sprintf("年份 %d 非法，同站同年整年未生效", k.year)
						default:
							reason = fmt.Sprintf("同站同年有 %d 条非法记录，整年未生效，本条一并拒收", len(bad))
						}
						res.Items[i] = HistoricalItemResult{
							Index: i, OK: false, Applied: false, Reason: reason}
						yr.Reasons = append(yr.Reasons, fmt.Sprintf("第 %d 条：%s", i, reason))
					}
				}
				res.Years = append(res.Years, yr)
				continue
			}

			// 组内合法：按 (站,日) 折叠为最后出现的一条。
			byDate := map[string]int{}
			for _, i := range idxs {
				if prev, dup := byDate[in[i].Date]; dup {
					res.Items[prev] = HistoricalItemResult{
						Index: prev, OK: true, Applied: false,
						Reason: fmt.Sprintf("同批中站 %s 日 %s 重复，本条被后一条覆盖",
							k.station, in[prev].Date)}
				}
				byDate[in[i].Date] = i
			}

			existing, err := tx.ListHistoricalYears(k.station)
			if err != nil {
				return err
			}
			for _, y := range existing {
				if y == k.year {
					yr.Replaced = true
					break
				}
			}

			rows := make([]model.HistoricalTemp, 0, len(byDate))
			for _, i := range idxs {
				if byDate[in[i].Date] != i {
					continue // 被同批后一条覆盖
				}
				rows = append(rows, model.HistoricalTemp{
					StationCode: k.station,
					Year:        k.year,
					Date:        parsed[i].d,
					TMax:        in[i].TMax,
					TMin:        in[i].TMin,
				})
				res.Items[i] = HistoricalItemResult{Index: i, OK: true, Applied: true}
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Date.Before(rows[j].Date) })

			if err := tx.ReplaceHistoricalYear(k.station, k.year, rows); err != nil {
				return err
			}
			yr.Applied = true
			yr.Days = len(rows)
			res.Years = append(res.Years, yr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
