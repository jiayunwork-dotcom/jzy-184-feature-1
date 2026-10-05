package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"agristation/internal/cum"
	"agristation/internal/ensemble"
	"agristation/internal/gdd"
	"agristation/internal/model"
)

// DefaultQuantiles 是范围查询默认给出的分位：偏早（1/10）、居中（1/2）、
// 偏晚（9/10）。
var DefaultQuantiles = []float64{0.1, 0.5, 0.9}

// ensembleBundle 是一次“往年试走”的完整中间结果，范围查询与比例查询共用。
type ensembleBundle struct {
	v            *model.Variety
	asOf         model.Date
	horizon      model.Date
	fullyReached bool // 五阶段本季全部实际达到，不需要历年资料
	reached      [stageCount]bool
	actualDate   [stageCount]*model.Date
	years        []int                          // 实际参与的历年（升序）
	arrivals     [][stageCount]ensemble.Arrival // [yearIndex][stageIndex]
}

const stageCount = 5 // 与 len(model.StageOrder) 一致；数组长度必须用常量

type futurePlanDay struct {
	date    model.Date
	station string // 空串表示当日无生效绑定（GDD=0）
	segment int    // 绑定站段+映射日历年的段号
	offset  int    // 映射日历年相对 asOf 日历年的偏移（0/1，跨年时为 1）
}

// StageRanges 查询地块各阶段在历年试走下的日期范围。
// quantiles 为空时用 DefaultQuantiles；每个 q 必须落在 [0,1]。
func (s *Service[T]) StageRanges(ctx context.Context, plotCode string,
	quantiles []float64, queryDate string) (*model.StageRangesResult, error) {

	if len(quantiles) == 0 {
		quantiles = DefaultQuantiles
	}
	qs := append([]float64(nil), quantiles...)
	sort.Float64s(qs)
	for _, q := range qs {
		if q < 0 || q > 1 {
			return nil, fmt.Errorf("分位必须在 [0,1] 之间，收到 %g", q)
		}
	}

	var res *model.StageRangesResult
	err := s.store.View(ctx, func(tx T) error {
		b, err := s.buildEnsemble(tx, plotCode, queryDate)
		if err != nil {
			return err
		}
		out := &model.StageRangesResult{
			PlotCode: plotCode, AsOf: b.asOf, Horizon: b.horizon,
			Available: b.fullyReached, // 全部达到时必然可用；否则看 answered
			Reason:    b.unavailableReason,
		}
		answered := 0
		for i, st := range model.StageOrder {
			rng := model.StageRange{
				Stage:     st,
				Threshold: b.v.Thresholds[i],
				YearsUsed: len(b.years),
				Years:     append([]int(nil), b.years...),
			}
			if b.reached[i] {
				d := *b.actualDate[i]
				rng.Available = true
				rng.Reached = true
				rng.ActualDate = &d
				rng.Earliest, rng.Latest = &d, &d
				for _, q := range qs {
					dd := d
					rng.Quantiles = append(rng.Quantiles, model.QuantilePoint{
						Quantile: q, Reachable: true, Date: &dd,
					})
				}
				answered++
				out.Ranges = append(out.Ranges, rng)
				continue
			}
			if len(b.years) == 0 {
				rng.Available = false
				rng.Reason = b.unavailableReason
				out.Ranges = append(out.Ranges, rng)
				continue
			}
			ordered := make([]ensemble.Arrival, len(b.arrivals))
			for yi := range b.arrivals {
				ordered[yi] = b.arrivals[yi][i]
			}
			ordered = ensemble.OrderedArrivals(ordered)
			if e := ensemble.Earliest(ordered); e != nil {
				rng.Earliest = e
			}
			if l := ensemble.Latest(ordered); l != nil {
				rng.Latest = l
			}
			rng.NotReachedYears = ensemble.NotReachedCount(ordered)
			for _, q := range qs {
				date, reachable := ensemble.Quantile(ordered, q)
				qp := model.QuantilePoint{Quantile: q, Reachable: reachable}
				if reachable {
					dd := date
					qp.Date = &dd
				}
				rng.Quantiles = append(rng.Quantiles, qp)
			}
			rng.Available = true
			answered++
			out.Ranges = append(out.Ranges, rng)
		}
		// 一个阶段都答不上来才整体标记不可用；若至少有阶段能答（例如
		// 早期阶段已达到），顶层仍可用，答不了的阶段各自带原因。
		out.Available = answered > 0
		res = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ArrivalBy 查询给定阶段在 by 当天或之前到达的年份比例。
// 已实际达到的阶段：by 早于实际日为 0，当天起为 1。
func (s *Service[T]) ArrivalBy(ctx context.Context, plotCode string,
	stage model.Stage, by, queryDate string) (*model.ArrivalResult, error) {

	stageIdx := -1
	for i, st := range model.StageOrder {
		if st == stage {
			stageIdx = i
		}
	}
	if stageIdx < 0 {
		return nil, fmt.Errorf("阶段名不存在：%q（可选 emergence/jointing/tasseling/silking/maturity）", stage)
	}
	byDate, err := time.Parse("2006-01-02", by)
	if err != nil {
		return nil, fmt.Errorf("日期格式应为 YYYY-MM-DD：%q", by)
	}
	byDate = byDate.UTC()

	var res *model.ArrivalResult
	err = s.store.View(ctx, func(tx T) error {
		p, err := tx.GetPlot(plotCode)
		if err != nil {
			return err
		}
		if p == nil {
			return notFoundError{what: "地块", code: plotCode}
		}
		if byDate.Before(p.SowDate) {
			return fmt.Errorf("查询日期 %s 早于播种日 %s",
				byDate.Format("2006-01-02"), p.SowDate.Format("2006-01-02"))
		}
		b, err := s.buildEnsemble(tx, plotCode, queryDate)
		if err != nil {
			return err
		}
		out := &model.ArrivalResult{
			PlotCode: plotCode, Stage: stage, By: byDate,
			AsOf: b.asOf, YearsUsed: len(b.years),
		}
		// 已实际达到的阶段不依赖历年资料：比例收成 0/1。
		if b.reached[stageIdx] {
			out.Available = true
			out.Reached = true
			d := *b.actualDate[stageIdx]
			out.ActualDate = &d
			if byDate.Before(d) {
				out.Fraction = 0
			} else {
				out.Fraction = 1
			}
			res = out
			return nil
		}
		if len(b.years) == 0 {
			out.Available = false
			out.Reason = b.unavailableReason
			res = out
			return nil
		}
		out.Available = true
		ordered := make([]ensemble.Arrival, len(b.arrivals))
		for yi := range b.arrivals {
			ordered[yi] = b.arrivals[yi][stageIdx]
		}
		out.Fraction = ensemble.FractionBy(ensemble.OrderedArrivals(ordered), byDate)
		res = out
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// bundleWithReason 附带“给不出范围”的原因。
type bundleWithReason struct {
	ensembleBundle
	unavailableReason string
}

// buildEnsemble 用最终已提交数据现算一次“往年试走”。它不读写任何增量
// 快照以外的缓存：范围与比例因此天然等于“只拿最终数据从头算一遍”，
// 观测更正、改绑、品种修改、历年导入/替换无论怎样交错，结果都一致。
func (s *Service[T]) buildEnsemble(tx T, plotCode, queryDate string) (*bundleWithReason, error) {
	p, err := tx.GetPlot(plotCode)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, notFoundError{what: "地块", code: plotCode}
	}
	v, err := tx.GetVariety(p.Variety)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("地块 %s 引用了不存在的品种 %s", plotCode, p.Variety)
	}
	method, err := gdd.ParseMethod(p.Method)
	if err != nil {
		return nil, err
	}
	asOf := s.nowDate()
	if queryDate != "" {
		qd, err := time.Parse("2006-01-02", queryDate)
		if err != nil {
			return nil, fmt.Errorf("查询日期格式应为 YYYY-MM-DD：%q", queryDate)
		}
		asOf = qd.UTC()
	}
	if err := ValidateSowQuery(p.SowDate, asOf); err != nil {
		return nil, err
	}
	bindings, err := tx.ListBindings(plotCode)
	if err != nil {
		return nil, err
	}
	allStations, err := tx.ListStations()
	if err != nil {
		return nil, err
	}
	stations := make(map[string]*model.Station, len(allStations))
	for i := range allStations {
		st := allStations[i]
		stations[st.Code] = &st
	}

	b := &bundleWithReason{}
	b.v = v
	b.asOf = asOf
	b.horizon = asOf.AddDate(0, 0, ForecastDays)

	// 当季已经走过的部分：直接读与现有单点功能同一条快照序列，
	// 拿到截至 asOf 的累计积温与已达到阶段/实际日期。
	snap, err := tx.ListDaily(plotCode)
	if err != nil {
		return nil, err
	}
	accSnap := make([]cum.Accumulated, 0, len(snap))
	for _, r := range snap {
		accSnap = append(accSnap, cum.Accumulated{
			Day: cum.Day{
				Date: r.Date, TMax: r.TMax, TMin: r.TMin, GDD: r.GDD,
				Source: r.Source, FillMethod: r.FillMethod, FillFrom: r.FillFrom,
			},
			Cumulative: r.Cumulative,
		})
	}
	stageNow := cum.Stages(accSnap, v, asOf)
	prefixCum := 0.0
	for _, r := range snap {
		if !r.Date.After(asOf) {
			prefixCum = r.Cumulative
		}
	}
	allReached := true
	for i, sr := range stageNow {
		if sr.Status == model.StatusReached && sr.Date != nil {
			b.reached[i] = true
			d := *sr.Date
			b.actualDate[i] = &d
		} else {
			allReached = false
		}
	}

	// 未来日的绑定计划与站段编号（站或映射日历年切换即开新段）。
	plan := make([]futurePlanDay, 0, ForecastDays)
	segID := -1
	var lastStation string
	lastOffset := -2
	stationSet := map[string]struct{}{}
	for d := asOf.AddDate(0, 0, 1); !d.After(b.horizon); d = d.AddDate(0, 0, 1) {
		day := futurePlanDay{date: d, offset: d.Year() - asOf.Year()}
		if ab := activeBindingAt(bindings, d); ab != nil {
			if _, ok := stations[ab.StationCode]; ok {
				day.station = ab.StationCode
				stationSet[ab.StationCode] = struct{}{}
			}
		}
		if day.station != lastStation || day.offset != lastOffset {
			segID++
			lastStation = day.station
			lastOffset = day.offset
		}
		day.segment = segID
		plan = append(plan, day)
	}

	// 五个阶段都已实际达到：不需要历年资料，范围收成实际日。
	if allReached {
		b.fullyReached = true
		return b, nil
	}
	if len(stationSet) == 0 {
		b.unavailableReason = "预测窗口内该地块没有任何生效绑定的气象站，无法试走"
		return b, nil
	}

	// 候选试走年份：未来区间出现过的绑定站各自“有资料年份”的交集。
	stList := make([]string, 0, len(stationSet))
	for st := range stationSet {
		stList = append(stList, st)
	}
	sort.Strings(stList)
	candidates := map[int]struct{}{}
	first := true
	minOff, maxOff := 0, 0
	for _, d := range plan {
		if d.offset < minOff {
			minOff = d.offset
		}
		if d.offset > maxOff {
			maxOff = d.offset
		}
	}
	for _, st := range stList {
		ys, err := tx.ListHistoricalYears(st)
		if err != nil {
			return nil, err
		}
		set := map[int]struct{}{}
		for _, y := range ys {
			set[y] = struct{}{}
		}
		if first {
			candidates = set
			first = false
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
		b.unavailableReason = "该地块预测窗口内的绑定站没有任何共同覆盖的历年逐日气温资料，给不出年际范围"
		return b, nil
	}

	// 一次性取候选年份映射日历年范围内、相关站的全部历史记录。
	minY, maxY := years[0]+minOff, years[len(years)-1]+maxOff
	histRows, err := tx.ListHistorical(stList,
		model.DateFrom(minY, time.January, 1), model.DateFrom(maxY, time.December, 31))
	if err != nil {
		return nil, err
	}
	hist := make(map[string]map[model.Date]model.HistoricalWeather, len(stList))
	for _, h := range histRows {
		m := hist[h.StationCode]
		if m == nil {
			m = map[model.Date]model.HistoricalWeather{}
			hist[h.StationCode] = m
		}
		m[h.Date] = h
	}

	// 未来日气候平均一次性批量预载（按 station -> DOY），避免按天回库。
	allNormals, err := tx.ListClimateNormals(stList)
	if err != nil {
		return nil, err
	}
	normals := make(map[string]map[int]*model.ClimateNormal, len(stList))
	for i := range allNormals {
		n := allNormals[i]
		m := normals[n.StationCode]
		if m == nil {
			m = map[int]*model.ClimateNormal{}
			normals[n.StationCode] = m
		}
		cp := n
		m[n.DOY] = &cp
	}
	// normalFor 按本季日期取气候平均，2/29 缺值回退 3/1（DOY 60）。
	normalFor := func(stCode string, d model.Date) *model.ClimateNormal {
		if n := normals[stCode][d.YearDay()]; n != nil {
			return n
		}
		if d.Month() == time.February && d.Day() == 29 {
			return normals[stCode][model.DateFrom(d.Year(), time.March, 1).YearDay()]
		}
		return nil
	}

	// 历史年与本季按“日序（DOY）”对齐：气候平均本来就是 DOY 序列，
	// “历史=气候平均 ⇒ 分位=单点”这一不变式要求两者取同一个年内日序。
	// 本季是闰年时 2/29（DOY 60）在平年候选年自然落到 3/1（DOY 60），
	// 与气候平均 2/29 回退 3/1 同口径；候选年是闰年时则取到其真实 2/29。
	mapDate := func(y int, d model.Date) model.Date {
		calY := y + d.Year() - asOf.Year()
		jan1 := model.DateFrom(calY, time.January, 1)
		return jan1.AddDate(0, 0, d.YearDay()-1)
	}

	cfg := ensemble.Config{
		Method:     method,
		BaseTemp:   v.BaseTemp,
		UpperTemp:  v.UpperTemp,
		PrefixCum:  prefixCum,
		Thresholds: v.Thresholds,
	}

	lastFailReason := ""
	failCount := 0
	for _, y := range years {
		fdays := make([]ensemble.FutureDay, 0, len(plan))
		for _, pd := range plan {
			fd := ensemble.FutureDay{Date: pd.date, Segment: pd.segment}
			if pd.station == "" {
				fd.Zero = true
				fdays = append(fdays, fd)
				continue
			}
			md := mapDate(y, pd.date)
			if h, ok := hist[pd.station][md]; ok {
				fd.Eligible, fd.Observed = true, true
				fd.TMax, fd.TMin = h.TMax, h.TMin
			} else if n := normalFor(pd.station, pd.date); n != nil {
				// 气候平均按“本季这一天”的日序取，与单点预测完全同一路径
				//（历史记录才按映射日历年日期取）；闰年 2/29 由 getNormal 回退 3/1。
				fd.Eligible = true
				fd.TMax, fd.TMin = n.TMax, n.TMin
			}
			fdays = append(fdays, fd)
		}
		ok, why := ensemble.YearEligible(fdays)
		if !ok {
			failCount++
			lastFailReason = fmt.Sprintf("%d 年：%s", y, why)
			continue
		}
		av := ensemble.SimulateYear(ensemble.YearInput{Year: y, Days: fdays}, cfg)
		var arr [stageCount]ensemble.Arrival
		copy(arr[:], av)
		b.years = append(b.years, y)
		b.arrivals = append(b.arrivals, arr)
	}

	if len(b.years) == 0 {
		reason := "候选历年均不满足试走条件"
		if failCount > 0 {
			reason += "（" + lastFailReason + "）"
		}
		b.unavailableReason = reason
	}
	return b, nil
}
