package enginemem

import (
	"sort"
	"time"

	"agristation/internal/fill"
	"agristation/internal/gdd"
	"agristation/internal/model"
	"agristation/internal/outlook"
)

// ReferenceOutlook 是集合试走的独立参考实现：不经过 engine 的任何路径，
// 直接读 mem 里的最终数据（观测、绑定、气候平均、历年资料），从播种日
// 逐日构建已发生段，再用每个历史年份把 asOf 次日起的窗口各试走一遍。
// 随机交错测试用它与 engine.PlotOutlook 的结果逐项对账。
//
// 缺日策略与 engine 一致：该年该日无历史行 -> 该站该日序气候平均
// （2/29 对齐非闰年时回退 3/1）-> 都没有则该日 GDD 记 0。
// 参与年份 = asOf 次日生效绑定站中、至少有一天历史行的年份。
func (m *Store) ReferenceOutlook(plotCode string, asOf time.Time, quantiles []float64) *model.Outlook {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(quantiles) == 0 {
		quantiles = []float64{0.1, 0.5, 0.9}
	}
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
	pastGDD := func(day time.Time, st model.Station) float64 {
		if o, ok := m.obs[obsKey{st.Code, dateKey(day)}]; ok {
			return gdd.Daily(method, o.TMax, o.TMin, v.BaseTemp, v.UpperTemp)
		}
		cands := make([]fill.Candidate, 0)
		for _, o := range m.obs {
			if o.Date.Equal(day) {
				if cs, ok2 := m.stations[o.StationCode]; ok2 {
					cands = append(cands, fill.Candidate{Station: cs, TMax: o.TMax, TMin: o.TMin})
				}
			}
		}
		if r := fill.Missing(st, cands, m.getNormal(st.Code, day)); r != nil {
			return gdd.Daily(method, r.TMax, r.TMin, v.BaseTemp, v.UpperTemp)
		}
		return 0
	}

	var past []float64
	cum0 := 0.0
	for day := p.SowDate; !day.After(asOf); day = day.AddDate(0, 0, 1) {
		g := 0.0
		if b := active(day); b != nil {
			g = pastGDD(day, m.stations[b.StationCode])
		}
		past = append(past, g)
		cum0 += g
	}

	firstDay := asOf.AddDate(0, 0, 1)
	horizon := asOf.AddDate(0, 0, ForecastDays)
	b0 := active(firstDay)

	out := &model.Outlook{PlotCode: plotCode, AsOf: dateKey(asOf), Horizon: dateKey(horizon)}
	if b0 == nil {
		return out
	}
	years := m.histYearsLocked(b0.StationCode)
	out.Stages = make([]model.StageOutlook, len(model.StageOrder))

	reachedDate := func(thr float64) *time.Time {
		if cum0+1e-9 < thr {
			return nil
		}
		total := 0.0
		d := p.SowDate
		for _, g := range past {
			total += g
			if total+1e-9 >= thr {
				dd := dateKey(d)
				return &dd
			}
			d = d.AddDate(0, 0, 1)
		}
		return nil
	}

	for si, st := range model.StageOrder {
		thr := v.Thresholds[si]
		so := model.StageOutlook{Stage: st, Threshold: thr, Quantiles: []model.Quantile{}}
		if rd := reachedDate(thr); rd != nil {
			d := *rd
			so.Status = model.StatusReached
			so.ActualDate = &d
			so.Earliest, so.Latest = &d, &d
			for _, q := range quantiles {
				dd := d
				so.Quantiles = append(so.Quantiles, model.Quantile{Q: q, Reachable: true, Date: &dd})
			}
			out.Stages[si] = so
			continue
		}
		so.Status = model.StatusForecast
		so.NYears = len(years)
		mems := make([]outlook.Member, 0, len(years))
		for _, y := range years {
			total := cum0
			hit := false
			hitOffset := 0
			off := int(firstDay.Sub(p.SowDate).Hours() / 24)
			for d := firstDay; !d.After(horizon); d = d.AddDate(0, 0, 1) {
				g := 0.0
				if b := active(d); b != nil {
					if _, ok := m.stations[b.StationCode]; ok {
						if tmax, tmin, have := m.memberTemp(b.StationCode, d, y, firstDay.Year()); have {
							g = gdd.Daily(method, tmax, tmin, v.BaseTemp, v.UpperTemp)
						}
					}
				}
				total += g
				if !hit && total+1e-9 >= thr {
					hit = true
					hitOffset = off
				}
				off++
			}
			mems = append(mems, outlook.Member{Year: y, Reached: hit, Days: hitOffset})
		}
		dist := outlook.NewDistribution(mems)
		so.UnreachedYears = dist.Unreached()
		if e, ok := dist.Earliest(); ok {
			dd := dateKey(p.SowDate.AddDate(0, 0, e))
			so.Earliest = &dd
		}
		if l, ok := dist.Latest(); ok {
			dd := dateKey(p.SowDate.AddDate(0, 0, l))
			so.Latest = &dd
		}
		for _, q := range quantiles {
			days, reachable, _ := dist.Quantile(q)
			qq := model.Quantile{Q: q, Reachable: reachable}
			if reachable {
				dd := dateKey(p.SowDate.AddDate(0, 0, days))
				qq.Date = &dd
			}
			so.Quantiles = append(so.Quantiles, qq)
		}
		out.Stages[si] = so
	}
	return out
}

// ReferenceProbability 参考实现：by 当天或之前达到该阶段的年数与比例。
// 返回 n=参与年数（已达到阶段为 0）、k=在 by 当天或之前达到的年数
// （已达到阶段给 0/1）、状态与实际达到日。
func (m *Store) ReferenceProbability(plotCode string, asOf, by time.Time, stage model.Stage) (
	n, k int, status model.StageStatus, actual *time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	thr := 0.0
	for i, s := range model.StageOrder {
		if s == stage {
			thr = v.Thresholds[i]
		}
	}
	cum0 := 0.0
	for day := p.SowDate; !day.After(asOf); day = day.AddDate(0, 0, 1) {
		g := 0.0
		if b := active(day); b != nil {
			st := m.stations[b.StationCode]
			if o, ok := m.obs[obsKey{b.StationCode, dateKey(day)}]; ok {
				g = gdd.Daily(method, o.TMax, o.TMin, v.BaseTemp, v.UpperTemp)
			} else {
				cands := make([]fill.Candidate, 0)
				for _, o := range m.obs {
					if o.Date.Equal(day) {
						if cs, ok2 := m.stations[o.StationCode]; ok2 {
							cands = append(cands, fill.Candidate{Station: cs, TMax: o.TMax, TMin: o.TMin})
						}
					}
				}
				if r := fill.Missing(st, cands, m.getNormal(b.StationCode, day)); r != nil {
					g = gdd.Daily(method, r.TMax, r.TMin, v.BaseTemp, v.UpperTemp)
				}
			}
		}
		cum0 += g
		if cum0+1e-9 >= thr && actual == nil {
			dd := dateKey(day)
			actual = &dd
		}
	}
	if actual != nil {
		status = model.StatusReached
		if !by.Before(*actual) {
			k = 1
		}
		return 0, k, status, actual
	}
	status = model.StatusForecast
	firstDay := asOf.AddDate(0, 0, 1)
	horizon := asOf.AddDate(0, 0, ForecastDays)
	b0 := active(firstDay)
	if b0 == nil {
		return 0, 0, status, nil
	}
	years := m.histYearsLocked(b0.StationCode)
	byDays := int(by.Sub(p.SowDate).Hours() / 24)
	for _, y := range years {
		total := cum0
		off := int(firstDay.Sub(p.SowDate).Hours() / 24)
		for d := firstDay; !d.After(horizon); d = d.AddDate(0, 0, 1) {
			g := 0.0
			if b := active(d); b != nil {
				if tmax, tmin, have := m.memberTemp(b.StationCode, d, y, firstDay.Year()); have {
					g = gdd.Daily(method, tmax, tmin, v.BaseTemp, v.UpperTemp)
				}
			}
			total += g
			if total+1e-9 >= thr {
				if off <= byDays {
					k++
				}
				break
			}
			off++
		}
	}
	return len(years), k, status, nil
}

// histYearsLocked 调用方持锁。返回某站有资料的年份升序。
func (m *Store) histYearsLocked(station string) []int {
	seen := map[int]struct{}{}
	for _, h := range m.hist {
		if h.StationCode == station {
			seen[h.Year] = struct{}{}
		}
	}
	years := make([]int, 0, len(seen))
	for y := range seen {
		years = append(years, y)
	}
	sort.Ints(years)
	return years
}

// memberTemp 历史行 -> 气候平均（含 2/29 对齐非闰年时回退 3/1）-> 无。
func (m *Store) memberTemp(station string, d time.Time, startYear, firstYear int) (float64, float64, bool) {
	delta := startYear - firstYear
	t := d.AddDate(delta, 0, 0)
	t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	if h, ok := m.hist[histKey{station, dateKey(t)}]; ok {
		return h.TMax, h.TMin, true
	}
	if n := m.getNormal(station, t); n != nil {
		return n.TMax, n.TMin, true
	}
	return 0, 0, false
}
