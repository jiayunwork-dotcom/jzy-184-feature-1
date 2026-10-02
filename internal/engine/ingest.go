package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"agristation/internal/model"
)

// ObservationInput 单条批量写入入参。
type ObservationInput struct {
	StationCode string  `json:"station_code"`
	Date        string  `json:"date"` // YYYY-MM-DD
	TMax        float64 `json:"tmax"`
	TMin        float64 `json:"tmin"`
	Seq         int     `json:"seq"`
}

// ObservationItemResult 单条写入结果。
type ObservationItemResult struct {
	Index  int    `json:"index"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	// Applied 为 true 表示该记录赢得该站日并真正进入计算；
	// false 表示序号更小被已有更新记录压制（仍算合法，只是未生效）。
	Applied bool `json:"applied"`
}

// IngestObservationsResult 批量写入结果。
type IngestObservationsResult struct {
	ChangeID string                  `json:"change_id,omitempty"` // 真正产生数据变化时非空
	Items    []ObservationItemResult `json:"items"`
}

// changeKey 一次观测变更的站日标识。
type changeKey struct {
	station string
	date    model.Date
}

// IngestObservations 批量写入观测（一次最多几千条）。
//
// 规则：
//   - 非法记录逐条拒绝并写明原因，其余照常写入；
//   - 同一站日序号大者胜出；晚到的小序号记录不覆盖，写入 stale 存档；
//   - 同站日完全相同序号+相同内容视为重复补传，幂等跳过；
//   - 并发：同站的所有写入在同一事务里对该站加 advisory lock，
//     最终保留序号最大的一条，地块只按胜出结果重算一次；
//   - 只有真正改变了某个站日胜出值的记录才触发重算，一次批处理共用一个
//     change_id，保证不产生重复事件。
func (s *Service[T]) IngestObservations(ctx context.Context, in []ObservationInput) (*IngestObservationsResult, error) {
	if len(in) == 0 {
		return &IngestObservationsResult{}, nil
	}
	if len(in) > 5000 {
		return nil, fmt.Errorf("单批最多 5000 条，当前 %d 条", len(in))
	}

	type parsedItem struct {
		idx int
		d   model.Date
		err error
	}
	parsed := make([]parsedItem, len(in))
	// 同批内先按 (站,日) 折叠：同站日多条只留序号最大的一条参与仲裁。
	best := map[changeKey]int{}
	stationsTouched := map[string]struct{}{}
	for i, item := range in {
		parsed[i].idx = i
		d, err := time.Parse("2006-01-02", item.Date)
		if err != nil {
			parsed[i].err = fmt.Errorf("日期格式应为 YYYY-MM-DD：%q", item.Date)
			continue
		}
		parsed[i].d = d.UTC()
		if item.Seq <= 0 {
			parsed[i].err = fmt.Errorf("上报序号必须为正整数，收到 %d", item.Seq)
			continue
		}
		if err := ValidateTemp(item.TMax, item.TMin); err != nil {
			parsed[i].err = err
			continue
		}
		k := changeKey{station: item.StationCode, date: parsed[i].d}
		if bi, ok := best[k]; ok {
			if item.Seq > in[bi].Seq {
				best[k] = i
			}
		} else {
			best[k] = i
			stationsTouched[item.StationCode] = struct{}{}
		}
	}
	locked := make([]string, 0, len(stationsTouched))
	for st := range stationsTouched {
		locked = append(locked, st)
	}
	sort.Strings(locked)

	res := &IngestObservationsResult{Items: make([]ObservationItemResult, len(in))}
	for i := range in {
		res.Items[i] = ObservationItemResult{Index: i}
	}
	for _, p := range parsed {
		if p.err != nil {
			res.Items[p.idx] = ObservationItemResult{Index: p.idx, OK: false, Reason: p.err.Error()}
		}
	}

	err := s.store.Update(ctx, func(tx T) error {
		// 对涉及到的站按代码排序加事务级排他咨询锁，序列化同站并发写入。
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

		changes := map[changeKey]struct{}{}

		for k, idx := range best {
			item := in[idx]
			d := parsed[idx].d
			if !stationOK[item.StationCode] {
				res.Items[idx] = ObservationItemResult{
					Index: idx, OK: false,
					Reason: fmt.Sprintf("站点 %s 不存在", item.StationCode),
				}
				continue
			}
			cur, err := tx.GetObservation(item.StationCode, d)
			if err != nil {
				return err
			}
			incoming := model.Observation{
				StationCode: item.StationCode,
				Date:        d,
				TMax:        item.TMax,
				TMin:        item.TMin,
				Seq:         item.Seq,
				UpdatedAt:   time.Now().UTC(),
			}
			if cur != nil {
				switch {
				case incoming.Seq < cur.Seq:
					// 晚到的小序号：存档但不覆盖。
					if err := tx.PutStaleObservation(incoming); err != nil {
						return err
					}
					res.Items[idx] = ObservationItemResult{Index: idx, OK: true, Applied: false,
						Reason: fmt.Sprintf("上报序号 %d 小于已有最新序号 %d，未覆盖", incoming.Seq, cur.Seq)}
					continue
				case incoming.Seq == cur.Seq && sameTemps(incoming, *cur):
					// 完全重复补传：幂等，无变化。
					res.Items[idx] = ObservationItemResult{Index: idx, OK: true, Applied: false,
						Reason: "与当前最新记录完全相同，重复上报忽略"}
					continue
				case incoming.Seq == cur.Seq:
					res.Items[idx] = ObservationItemResult{
						Index: idx, OK: false,
						Reason: fmt.Sprintf("站 %s 日 %s 已有序号 %d 的记录，相同序号内容不一致",
							item.StationCode, item.Date, cur.Seq),
					}
					continue
				default:
					// 新序号更大：旧记录转存档，新记录胜出。
					if err := tx.PutStaleObservation(*cur); err != nil {
						return err
					}
				}
			}
			if err := tx.PutObservation(incoming); err != nil {
				return err
			}
			changes[k] = struct{}{}
			res.Items[idx] = ObservationItemResult{Index: idx, OK: true, Applied: true}
		}

		// 批内同站日被更大序号压制的其他条目。
		for i, item := range in {
			if parsed[i].err != nil || res.Items[i].Reason != "" {
				continue
			}
			k := changeKey{station: item.StationCode, date: parsed[i].d}
			if best[k] != i {
				res.Items[i] = ObservationItemResult{Index: i, OK: true, Applied: false,
					Reason: fmt.Sprintf("同批中站 %s 日 %s 已存在序号不小于本条的上报", item.StationCode, item.Date)}
			}
		}

		if len(changes) == 0 {
			return nil
		}

		changeID := s.newID()
		res.ChangeID = changeID
		asOf := s.nowDate()

		chList := make([]changeKey, 0, len(changes))
		for c := range changes {
			chList = append(chList, c)
		}
		sort.Slice(chList, func(i, j int) bool {
			if chList[i].date.Equal(chList[j].date) {
				return chList[i].station < chList[j].station
			}
			return chList[i].date.Before(chList[j].date)
		})

		// 受影响地块重算。注意：变化的站日不仅影响绑定该站的地块——
		// 其他地块在这一天可能把该站当作邻站补值来源。某日 GDD 只依赖
		// 同日温度，因此对每个地块从“序列内最早变化日”起重算即可，
		// 既不漏掉补值传播，也不必回到播种日。
		plots, err := tx.ListPlots()
		if err != nil {
			return err
		}
		for _, p := range plots {
			plot := p
			earliest := model.Date{}
			cds := make([]changeKey, 0)
			for _, c := range chList {
				if !c.date.Before(plot.SowDate) && !c.date.After(asOf.AddDate(0, 0, ForecastDays)) {
					cds = append(cds, c)
					if earliest.IsZero() || c.date.Before(earliest) {
						earliest = c.date
					}
				}
			}
			if len(cds) == 0 {
				continue
			}
			if err := s.recomputePlot(tx, &plot, asOf, earliest, changeID, changesReason(cds), false); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// lockStations 对站点加事务级排他咨询锁（锁顺序为站代码升序，避免死锁）。
func lockStations(tx Tx, codes []string) error {
	l, ok := tx.(interface{ LockStations([]string) error })
	if !ok {
		return nil
	}
	return l.LockStations(codes)
}

func sameTemps(a, b model.Observation) bool {
	const t = 1e-9
	return absF(a.TMax-b.TMax) < t && absF(a.TMin-b.TMin) < t
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func changesReason(cds []changeKey) string {
	msg := "观测数据变化："
	for i, c := range cds {
		if i >= 5 {
			msg += fmt.Sprintf("等 %d 个站日", len(cds))
			break
		}
		if i > 0 {
			msg += "；"
		}
		msg += fmt.Sprintf("站 %s %s 数据变化", c.station, c.date.Format("01-02"))
	}
	return msg
}
