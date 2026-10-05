package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"agristation/internal/model"
)

// HistoricalInput 单条历年逐日气温记录。
type HistoricalInput struct {
	StationCode string  `json:"station_code"`
	Year        int     `json:"year,omitempty"` // 可空：取 date 的年份
	Date        string  `json:"date"`           // YYYY-MM-DD
	TMax        float64 `json:"tmax"`
	TMin        float64 `json:"tmin"`
}

// HistoricalItemResult 单条历年记录的处理结果。
type HistoricalItemResult struct {
	Index       int    `json:"index"`
	StationCode string `json:"station_code,omitempty"`
	Year        int    `json:"year,omitempty"`
	OK          bool   `json:"ok"`
	Reason      string `json:"reason,omitempty"`
	// Applied 表示该记录所属的站年整年通过校验并已整体生效。
	// 合法但因同站同年另有非法记录而整年不生效时为 false。
	Applied bool `json:"applied"`
}

// HistoricalYearResult 一个站年整体的处理结果。
type HistoricalYearResult struct {
	StationCode string `json:"station_code"`
	Year        int    `json:"year"`
	Replaced    bool   `json:"replaced"`
	Records     int    `json:"records"`
	Reason      string `json:"reason,omitempty"`
}

// IngestHistoricalResult 批量导入历年资料的结果。
type IngestHistoricalResult struct {
	Items []HistoricalItemResult `json:"items"`
	Years []HistoricalYearResult `json:"years"`
}

type histGroupKey struct {
	station string
	year    int
}

type histParsed struct {
	d   model.Date
	err error
}

// MaxHistoricalBatch 单次导入记录数上限。
const MaxHistoricalBatch = 20000

// IngestHistorical 按“站+年”整年导入或替换逐日最高最低气温。
//
// 规则：
//   - 一次可带多个站、多个年；非法记录逐条报原因，其余站年照常生效；
//   - 同一站同一年是一个整体：其中任何一条非法（最低>最高、超范围、
//     站点不存在、日期不属于所标年份、同批同站同日重复），整年不生效；
//   - 同一站同一年再导一次即整年替换旧内容；
//   - 整年替换在一个数据库事务内完成，提交前对外不可见：服务在导入
//     途中被杀，重启后仍是旧的一整年，查不到半年数据；
//   - 同站的并发导入先取 advisory lock 串行化，因此同站同年两个并发
//     导入最终必定完整等于其中一次。
//
// 历年资料只供“往年试走”的范围查询使用，不参与现有单点预测、逐日
// 快照与阶段事件：导了历年资料不会悄悄改变原有任何结果。
func (s *Service[T]) IngestHistorical(ctx context.Context, in []HistoricalInput) (*IngestHistoricalResult, error) {
	if len(in) == 0 {
		return &IngestHistoricalResult{}, nil
	}
	if len(in) > MaxHistoricalBatch {
		return nil, fmt.Errorf("单批最多 %d 条，当前 %d 条", MaxHistoricalBatch, len(in))
	}

	parsed := make([]histParsed, len(in))
	items := make([]HistoricalItemResult, len(in))
	for i := range in {
		items[i] = HistoricalItemResult{Index: i, StationCode: in[i].StationCode, Year: in[i].Year}
	}

	// 第一遍：逐条解析/校验并归组。日期可解析的记录都会进所属站年组，
	// 非法与否由 parsed[i].err 携带——这样非法记录也能把它所在的站年
	// 标脏，而不是在组里“消失”导致站年被误判为干净。
	groups := map[histGroupKey][]int{}
	stationsTouched := map[string]struct{}{}
	dupSeen := map[histDayKey]int{} // 同批同站同日的首条下标
	for i, r := range in {
		d, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			parsed[i].err = fmt.Errorf("日期格式应为 YYYY-MM-DD：%q", r.Date)
			items[i].Reason = parsed[i].err.Error()
			continue
		}
		d = d.UTC()
		year := r.Year
		if year == 0 {
			year = d.Year()
			items[i].Year = year
		} else if year != d.Year() {
			parsed[i].err = fmt.Errorf("日期 %s 不属于所标的年份 %d", r.Date, year)
			items[i].Reason = parsed[i].err.Error()
			continue
		}
		if year < 1900 || year > 2100 {
			parsed[i].err = fmt.Errorf("年份 %d 超出支持范围 [1900,2100]", year)
			items[i].Reason = parsed[i].err.Error()
			continue
		}
		parsed[i].d = d
		k := histGroupKey{station: r.StationCode, year: year}
		groups[k] = append(groups[k], i)
		stationsTouched[r.StationCode] = struct{}{}

		// 温度校验：非法记录仍留在组里（上面已 append），只标错误。
		if err := ValidateTemp(r.TMax, r.TMin); err != nil {
			parsed[i].err = err
			items[i].Reason = err.Error()
		}

		// 同批同站同日重复：两条都标非法，留在组里把站年标脏。
		dk := histDayKey{station: r.StationCode, date: d}
		if first, ok := dupSeen[dk]; ok {
			msg := fmt.Sprintf("同批中站 %s %s 重复出现（与第 %d 条冲突）",
				r.StationCode, r.Date, first)
			parsed[i].err = fmt.Errorf("%s", msg)
			items[i].Reason = msg
			if parsed[first].err == nil {
				parsed[first].err = fmt.Errorf("%s", msg)
			}
			if items[first].Reason == "" {
				items[first].Reason = msg
			}
			continue
		}
		dupSeen[dk] = i
	}

	locked := make([]string, 0, len(stationsTouched))
	for st := range stationsTouched {
		locked = append(locked, st)
	}
	sort.Strings(locked)

	res := &IngestHistoricalResult{Items: items}

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

		// 按组判定是否干净；组键排序保证替换顺序确定。
		keys := make([]histGroupKey, 0, len(groups))
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].station != keys[j].station {
				return keys[i].station < keys[j].station
			}
			return keys[i].year < keys[j].year
		})

		for _, k := range keys {
			idxs := groups[k]
			yr := HistoricalYearResult{StationCode: k.station, Year: k.year}
			clean := true
			for _, i := range idxs {
				if parsed[i].err != nil {
					clean = false
				}
			}
			if !stationOK[k.station] {
				clean = false
				reason := fmt.Sprintf("站点 %s 不存在", k.station)
				for _, i := range idxs {
					if items[i].Reason == "" {
						items[i].OK = false
						items[i].Reason = reason
					}
				}
				yr.Reason = reason
				res.Years = append(res.Years, yr)
				continue
			}
			if !clean {
				// 整年不生效：本身非法的条目 ok=false；本身合法但被同组
				// 非法记录连累（如重复日的另一方）ok=true、applied=false。
				badReason := ""
				for _, i := range idxs {
					if parsed[i].err != nil {
						badReason = items[i].Reason
						items[i].OK = false
						items[i].Applied = false
					}
				}
				for _, i := range idxs {
					if parsed[i].err == nil {
						items[i].OK = true
						items[i].Applied = false
						if items[i].Reason == "" {
							items[i].Reason = fmt.Sprintf(
								"同站 %s 同年 %d 存在非法记录（%s），该站年整体未导入",
								k.station, k.year, badReason)
						}
					}
				}
				yr.Reason = "站年内有非法记录，整年未生效"
				res.Years = append(res.Years, yr)
				continue
			}

			rows := make([]model.HistoricalWeather, 0, len(idxs))
			for _, i := range idxs {
				r := in[i]
				rows = append(rows, model.HistoricalWeather{
					StationCode: k.station,
					Year:        k.year,
					Date:        parsed[i].d,
					TMax:        r.TMax,
					TMin:        r.TMin,
				})
				items[i].OK = true
				items[i].Applied = true
			}
			sort.Slice(rows, func(i, j int) bool { return rows[i].Date.Before(rows[j].Date) })
			if err := tx.ReplaceHistoricalStationYear(k.station, k.year, rows); err != nil {
				return err
			}
			yr.Replaced = true
			yr.Records = len(rows)
			res.Years = append(res.Years, yr)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// histDayKey 同批去重用的站日键。
type histDayKey struct {
	station string
	date    model.Date
}
