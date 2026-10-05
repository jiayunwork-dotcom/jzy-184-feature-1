package enginemem

import (
	"sort"
	"time"

	"agristation/internal/cum"
	"agristation/internal/gdd"
	"agristation/internal/model"
)

// RefStageEnsemble 是参考实现给出的单阶段历年试走结果。
// OrderedDates 已按到达日升序（未到年排在最后，用 nil 表示）。
type RefStageEnsemble struct {
	Reached      bool
	Actual       *time.Time
	OrderedDates []*time.Time
}

// ReferenceEnsembleResult 是独立于 engine.buildEnsemble 的参考试走结果，
// 随机一致性测试用它逐项对账。
type ReferenceEnsembleResult struct {
	AsOf, Horizon time.Time
	Available     bool // 至少一个阶段答得上来（全部达到也算）
	FullyReached  bool
	Years         []int
	Stages        [5]RefStageEnsemble
}

type refPlanDay struct {
	date    time.Time
	station string
	segment int
	offset  int
}

// ReferenceEnsemble 用最终数据独立做一遍“往年试走”。它不读 engine 的任何
// 中间结构，连候选年份、段划分、分位排序都在这里另写一遍，作为范围/比例
// 查询必须等于“只拿最终数据从头算一遍”的对照基准。
func (m *Store) ReferenceEnsemble(plotCode string, asOf time.Time) *ReferenceEnsembleResult {
	m.mu.Lock()
	defer m.mu.Unlock()

	asOf = dateKey(asOf)
	p := m.plots[plotCode]
	v := m.varieties[p.Variety]
	method, _ := gdd.ParseMethod(p.Method)
	horizon := asOf.AddDate(0, 0, ForecastDays)

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

	// 当季已走部分：直接用独立的逐日参考实现。
	rows := m.referenceDailyLocked(plotCode, asOf)
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
	stageRes := cum.Stages(acc, &v, asOf)
	res := &ReferenceEnsembleResult{AsOf: asOf, Horizon: horizon}
	prefixCum := 0.0
	allReached := true
	for i, sr := range stageRes {
		if sr.Status == model.StatusReached && sr.Date != nil {
			d := *sr.Date
			res.Stages[i].Reached = true
			res.Stages[i].Actual = &d
		} else {
			allReached = false
		}
	}
	for _, r := range rows {
		if !r.Date.After(asOf) {
			prefixCum = r.Cumulative
		}
	}
	if allReached {
		res.Available = true
		res.FullyReached = true
		return res
	}

	// 未来计划与站段（与 engine 同一规则，另写一遍）。
	plan := []refPlanDay{}
	segID := -1
	var lastStation string
	lastOffset := -2
	stationSet := map[string]struct{}{}
	minOff, maxOff := 0, 0
	for d := asOf.AddDate(0, 0, 1); !d.After(horizon); d = d.AddDate(0, 0, 1) {
		day := refPlanDay{date: d, offset: d.Year() - asOf.Year()}
		if b := active(d); b != nil {
			if _, ok := m.stations[b.StationCode]; ok {
				day.station = b.StationCode
				stationSet[b.StationCode] = struct{}{}
			}
		}
		if day.station != lastStation || day.offset != lastOffset {
			segID++
			lastStation = day.station
			lastOffset = day.offset
		}
		day.segment = segID
		if day.offset < minOff {
			minOff = day.offset
		}
		if day.offset > maxOff {
			maxOff = day.offset
		}
		plan = append(plan, day)
	}
	if len(stationSet) == 0 {
		return res
	}

	// 候选年份：每个未来绑定站都有资料的日历年交集。
	stList := make([]string, 0, len(stationSet))
	for st := range stationSet {
		stList = append(stList, st)
	}
	sort.Strings(stList)
	yearsByStation := func(st string) map[int]struct{} {
		set := map[int]struct{}{}
		for k := range m.historical {
			if k.st == st {
				set[k.d.Year()] = struct{}{}
			}
		}
		return set
	}
	candidates := map[int]struct{}{}
	for i, st := range stList {
		set := yearsByStation(st)
		if i == 0 {
			candidates = set
		} else {
			for y := range candidates {
				if _, ok := set[y]; !ok {
					delete(candidates, y)
				}
			}
		}
	}
	years := make([]int, 0, len(candidates))
	for y := range candidates {
		years = append(years, y)
	}
	sort.Ints(years)
	if len(years) == 0 {
		return res
	}

	// 按 DOY 对齐（见 engine/ranges.go）。
	mapDate := func(y int, d time.Time) time.Time {
		calY := y + d.Year() - asOf.Year()
		return time.Date(calY, time.January, 1, 0, 0, 0, 0, time.UTC).
			AddDate(0, 0, d.YearDay()-1)
	}

	type simDay struct {
		date          time.Time
		zero          bool
		eligible, obs bool
		tmax, tmin    float64
		segment       int
	}
	usedYears := make([]int, 0)
	type yearArrivals [5]struct {
		reached bool
		date    time.Time
	}
	allArrivals := []yearArrivals{}

	for _, y := range years {
		days := make([]simDay, 0, len(plan))
		good := true
		segExists, segObs := map[int]bool{}, map[int]bool{}
		for _, pd := range plan {
			sd := simDay{date: pd.date, segment: pd.segment}
			if pd.station == "" {
				sd.zero = true
				days = append(days, sd)
				continue
			}
			md := mapDate(y, pd.date)
			if h, ok := m.historical[histKey{pd.station, dateKey(md)}]; ok {
				sd.eligible, sd.obs = true, true
				sd.tmax, sd.tmin = h.TMax, h.TMin
			} else if n := m.getNormal(pd.station, pd.date); n != nil {
				sd.eligible = true
				sd.tmax, sd.tmin = n.TMax, n.TMin
			} else {
				good = false
			}
			segExists[pd.segment] = true
			if sd.obs {
				segObs[pd.segment] = true
			}
			days = append(days, sd)
		}
		for seg := range segExists {
			if !segObs[seg] {
				good = false
			}
		}
		if !good {
			continue
		}
		total := prefixCum
		var ar yearArrivals
		hit := [5]bool{}
		for _, d := range days {
			if !d.zero {
				total += gdd.Daily(method, d.tmax, d.tmin, v.BaseTemp, v.UpperTemp)
			}
			for ti := range v.Thresholds {
				if !hit[ti] && total+1e-9 >= v.Thresholds[ti] {
					hit[ti] = true
					ar[ti].reached = true
					ar[ti].date = d.date
				}
			}
		}
		usedYears = append(usedYears, y)
		allArrivals = append(allArrivals, ar)
	}

	res.Years = usedYears
	if len(usedYears) > 0 {
		res.Available = true
	}
	for ti := range res.Stages {
		if res.Stages[ti].Reached {
			continue
		}
		if len(usedYears) == 0 {
			continue
		}
		type ca struct {
			reached bool
			date    time.Time
			year    int
		}
		ordered := make([]ca, len(allArrivals))
		for yi := range allArrivals {
			ordered[yi] = ca{
				reached: allArrivals[yi][ti].reached,
				date:    allArrivals[yi][ti].date,
				year:    usedYears[yi],
			}
		}
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].reached != ordered[j].reached {
				return ordered[i].reached
			}
			if !ordered[i].reached {
				return ordered[i].year < ordered[j].year
			}
			if !ordered[i].date.Equal(ordered[j].date) {
				return ordered[i].date.Before(ordered[j].date)
			}
			return ordered[i].year < ordered[j].year
		})
		res.Stages[ti].OrderedDates = make([]*time.Time, len(ordered))
		for i := range ordered {
			if ordered[i].reached {
				d := ordered[i].date
				res.Stages[ti].OrderedDates[i] = &d
			}
		}
	}
	return res
}
