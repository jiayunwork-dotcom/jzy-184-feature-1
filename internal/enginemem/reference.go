// Package enginemem 还提供一个“参考重算器”：不经过任何增量快照路径，
// 直接读最终数据（观测胜出表、绑定、气候平均），从播种日逐日构建地块序列。
// 测试用它作为增量结果必须逐日相等的对照基准。
package enginemem

import (
	"time"

	"agristation/internal/cum"
	"agristation/internal/fill"
	"agristation/internal/gdd"
	"agristation/internal/model"
)

// ForecastDays 与 engine.ForecastDays 保持一致。
const ForecastDays = 270

// ReferenceDaily 用最终数据从播种日逐日重算某地块。
func (m *Store) ReferenceDaily(plotCode string, asOf time.Time) []model.DailyValue {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.referenceDailyLocked(plotCode, asOf)
}

// referenceDailyLocked 与 ReferenceDaily 相同，但不加锁，供同一把锁内复用。
func (m *Store) referenceDailyLocked(plotCode string, asOf time.Time) []model.DailyValue {
	p := m.plots[plotCode]
	v := m.varieties[p.Variety]
	method, _ := gdd.ParseMethod(p.Method)

	bindings := append([]model.Binding(nil), m.bindings[plotCode]...)
	active := func(day time.Time) *model.Binding {
		var a *model.Binding
		for i := range bindings {
			if !bindings[i].EffectiveDate.After(day) {
				if a == nil || bindings[i].EffectiveDate.After(a.EffectiveDate) {
					a = &bindings[i]
				}
			}
		}
		return a
	}

	horizon := asOf.AddDate(0, 0, ForecastDays)
	rows := make([]model.DailyValue, 0)
	var total float64
	for day := p.SowDate; !day.After(horizon); day = day.AddDate(0, 0, 1) {
		row := model.DailyValue{PlotCode: plotCode, Date: day, AsOf: asOf}
		if b := active(day); b != nil {
			st := m.stations[b.StationCode]
			if !day.After(asOf) {
				if o, ok := m.obs[obsKey{b.StationCode, dateKey(day)}]; ok {
					row.TMax, row.TMin = &o.TMax, &o.TMin
					row.GDD = gdd.Daily(method, o.TMax, o.TMin, v.BaseTemp, v.UpperTemp)
					row.Source = model.SourceObserved
				} else {
					cands := make([]fill.Candidate, 0)
					for _, o := range m.obs {
						if o.Date.Equal(day) {
							if cs := m.stations[o.StationCode]; cs.Code != "" {
								cands = append(cands, fill.Candidate{
									Station: cs, TMax: o.TMax, TMin: o.TMin,
								})
							}
						}
					}
					if r := fill.Missing(st, cands, m.getNormal(b.StationCode, day)); r != nil {
						row.TMax, row.TMin = &r.TMax, &r.TMin
						row.GDD = gdd.Daily(method, r.TMax, r.TMin, v.BaseTemp, v.UpperTemp)
						row.Source = model.SourceFilled
						row.FillMethod = r.Method
						row.FillFrom = r.FromStation
					} else {
						row.Source = model.SourceMissing
					}
				}
			} else if n := m.getNormal(b.StationCode, day); n != nil {
				row.TMax, row.TMin = &n.TMax, &n.TMin
				row.GDD = gdd.Daily(method, n.TMax, n.TMin, v.BaseTemp, v.UpperTemp)
				row.Source = model.SourceClimate
			} else {
				row.Source = model.SourceMissing
			}
		} else {
			row.Source = model.SourceMissing
		}
		total += row.GDD
		row.Cumulative = total
		rows = append(rows, row)
	}
	return rows
}

// ReferenceStages 在参考序列上推算阶段日期。
func (m *Store) ReferenceStages(plotCode string, asOf time.Time) []cum.StageResult {
	rows := m.ReferenceDaily(plotCode, asOf)
	acc := make([]cum.Accumulated, 0, len(rows))
	for _, r := range rows {
		acc = append(acc, cum.Accumulated{
			Day: cum.Day{
				Date: r.Date, TMax: r.TMax, TMin: r.TMin, GDD: r.GDD,
				Source: r.Source, FillMethod: r.FillMethod, FillFrom: r.FillFrom,
			},
			Cumulative: r.Cumulative,
		})
	}
	v := m.varieties[m.plots[plotCode].Variety]
	return cum.Stages(acc, &v, asOf)
}

func (m *Store) getNormal(station string, day time.Time) *model.ClimateNormal {
	if n, ok := m.normals[normalKey{station, day.YearDay()}]; ok {
		cp := n
		return &cp
	}
	if day.Month() == time.February && day.Day() == 29 {
		mar1 := time.Date(day.Year(), time.March, 1, 0, 0, 0, 0, time.UTC)
		if n, ok := m.normals[normalKey{station, mar1.YearDay()}]; ok {
			cp := n
			return &cp
		}
	}
	return nil
}
