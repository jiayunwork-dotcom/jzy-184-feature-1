package engine

import (
	"context"
	"fmt"
	"time"

	"agristation/internal/cum"
	"agristation/internal/fill"
	"agristation/internal/gdd"
	"agristation/internal/model"
	"agristation/internal/outlook"
)

// dayOffset 返回 b 相对 a 的整天数（a、b 均为 UTC 零点）。
func dayOffset(a, b model.Date) int {
	y, m, d := b.Date()
	y0, m0, d0 := a.Date()
	t1 := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	t0 := time.Date(y0, m0, d0, 0, 0, 0, 0, time.UTC)
	return int(t1.Sub(t0).Hours() / 24)
}

// DefaultQuantiles 是试走范围接口默认返回的分位：偏早 0.1、居中 0.5、
// 偏晚 0.9。调用方也可以显式给任意 [0,1] 内的分位。
var DefaultQuantiles = []float64{0.1, 0.5, 0.9}

// stageThreshold 返回品种某阶段的累计积温需求。
func stageThreshold(v *model.Variety, st model.Stage) (float64, error) {
	for i, s := range model.StageOrder {
		if s == st {
			if i >= len(v.Thresholds) {
				return 0, fmt.Errorf("品种 %s 缺少阶段 %s 的积温需求", v.Code, st)
			}
			return v.Thresholds[i], nil
		}
	}
	return 0, fmt.Errorf("阶段名不存在：%q", string(st))
}

func parseStageName(name string) (model.Stage, error) {
	if name == "" {
		return "", fmt.Errorf("必须指定阶段名")
	}
	for _, s := range model.StageOrder {
		if string(s) == name {
			return s, nil
		}
	}
	return "", fmt.Errorf("阶段名不存在：%q", name)
}

// walkContext 是一次集合试走的共享上下文：地块、品种、口径、绑定、
// 站点档案、asOf 与预测窗口。
type walkContext struct {
	plot     *model.Plot
	variety  *model.Variety
	method   gdd.Method
	bindings []model.Binding
	stations map[string]*model.Station
	asOf     model.Date
	firstDay model.Date // asOf 次日
	horizon  model.Date // asOf + ForecastDays
}

// preparedWalk 是加载完数据、可反复“试走”的状态：共享的已发生段累计，
// 加上各候选站的历年资料与气候平均缓存。
type preparedWalk struct {
	wc walkContext
	// pastDays 播种日..asOf 的逐日（与确定性快照同口径构建）。
	pastDays []cum.Day
	// cumAtAsOf asOf 日收盘累计积温。
	cumAtAsOf float64
	// firstStation asOf 次日生效的绑定站；决定参与年份集合。
	firstStation *model.Station
	// years 参与试走的年份（firstStation 有资料的年份，升序）。
	years []int
	// hist[station][date] 历年行。
	hist map[string]map[time.Time]model.HistoricalTemp
	// normals[station][doy] 气候平均。
	normals map[string]map[int]model.ClimateNormal
}

// prepareWalk 为地块加载试走所需的全部最终数据（在一个只读事务内）。
// 查询日期 q 为空或非法日期由调用方先校验。
func (s *Service[T]) prepareWalk(tx T, plot *model.Plot, q model.Date) (*preparedWalk, error) {
	v, err := tx.GetVariety(plot.Variety)
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, fmt.Errorf("地块 %s 引用了不存在的品种 %s", plot.Code, plot.Variety)
	}
	method, err := gdd.ParseMethod(plot.Method)
	if err != nil {
		return nil, err
	}
	bindings, err := tx.ListBindings(plot.Code)
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

	firstDay := q.AddDate(0, 0, 1)
	b0 := activeBindingAt(bindings, firstDay)
	var firstStation *model.Station
	if b0 != nil {
		firstStation = stations[b0.StationCode]
	}

	wc := walkContext{
		plot: plot, variety: v, method: method, bindings: bindings,
		stations: stations, asOf: q, firstDay: firstDay,
		horizon: q.AddDate(0, 0, ForecastDays),
	}

	// 已发生段：播种日..asOf，与确定性预测完全同口径（观测→邻站补值
	// →气候平均→missing 记 0）。这保证“历史年每天都与气候平均相同”时
	// 集合结果与原单点预计逐日相等，也保证已达到阶段不受历年资料影响。
	pastDays := make([]cum.Day, 0)
	cum0 := 0.0
	for d := plot.SowDate; !d.After(q); d = d.AddDate(0, 0, 1) {
		day := s.buildDay(tx, plot, v, method, bindings, stations, d, q)
		cum0 += day.GDD
		pastDays = append(pastDays, day)
	}

	pw := &preparedWalk{
		wc:           wc,
		pastDays:     pastDays,
		cumAtAsOf:    cum0,
		firstStation: firstStation,
		hist:         map[string]map[time.Time]model.HistoricalTemp{},
		normals:      map[string]map[int]model.ClimateNormal{},
	}

	// 预加载窗口内各绑定站的历年资料与气候平均。
	needed := map[string]struct{}{}
	if firstStation != nil {
		needed[firstStation.Code] = struct{}{}
	}
	for _, b := range bindings {
		if !b.EffectiveDate.After(wc.horizon) {
			needed[b.StationCode] = struct{}{}
		}
	}
	for code := range needed {
		rows, err := tx.ListHistoricalStationRows(code)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			m := make(map[time.Time]model.HistoricalTemp, len(rows))
			for _, r := range rows {
				m[utcDate(r.Date)] = r
			}
			pw.hist[code] = m
		}
		ns, err := tx.ListNormals(code)
		if err != nil {
			return nil, err
		}
		if len(ns) > 0 {
			nm := make(map[int]model.ClimateNormal, len(ns))
			for _, n := range ns {
				nm[n.DOY] = n
			}
			pw.normals[code] = nm
		}
	}

	if firstStation != nil {
		pw.years = sortedYears(pw.hist[firstStation.Code])
	}
	return pw, nil
}

func utcDate(d model.Date) time.Time {
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
}

func sortedYears(m map[time.Time]model.HistoricalTemp) []int {
	seen := map[int]struct{}{}
	for _, r := range m {
		seen[r.Year] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for y := range seen {
		out = append(out, y)
	}
	// 简单插入排序（年份数量有限）。
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// alignDate 把预测日 d 按“同月同日”平移到成员年：平移量固定为
// startYear - firstDay.Year()（整个试走共用）。不能逐日按 d.Year() 算
// 平移量——预测窗口跨年时（如 2026-06 起、2027-02 止），逐日算会把
// 窗口后半的 2007 年日期错映回成员年初的 1、2 月，等于把该年从头
// 又走一遍。固定平移后 2026-06-02→2004-06-02、2007-01-16→2005-01-16，
// 成员次年没有资料就按缺日策略处理（气候平均或记 0）。
// 目标年没有 2/29 时 Go 会归一到 3/1，与气候平均的闰年回退口径一致。
func alignDate(d model.Date, startYear, firstYear int) time.Time {
	t := d.AddDate(startYear-firstYear, 0, 0)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// memberTemp 取成员年份在预测日 d 的温度。
//
// 缺日策略（详见 docs/design.md 第 7 节）：该年该日没有历史行时，用该
// 绑定站该日序的气候平均；连气候平均都没有则不编造，该日积温记 0。
// 不借邻站同年：邻站补值反映的是“当年天气过程的空间同步”，把它搬到
// 三十年前的另一年没有依据；历史缺档只在本站内部解决。
func (pw *preparedWalk) memberTemp(station string, d model.Date, startYear int) (tmax, tmin float64, have bool) {
	if hm := pw.hist[station]; hm != nil {
		if r, ok := hm[alignDate(d, startYear, pw.wc.firstDay.Year())]; ok {
			return r.TMax, r.TMin, true
		}
	}
	nd := alignDate(d, startYear, pw.wc.firstDay.Year())
	if n, ok := pw.normals[station][fill.NormalDOY(nd)]; ok {
		return n.TMax, n.TMin, true
	}
	if nd.Month() == time.February && nd.Day() == 29 {
		// 2/29 无气候平均：回退 3/1（doy 61），与 getNormal 口径一致。
		if n, ok := pw.normals[station][61]; ok {
			return n.TMax, n.TMin, true
		}
	}
	return 0, 0, false
}

// crossing 是一次试走在单个阶段上的结果：reached=false 表示窗口内没到。
type crossing struct {
	stage   model.Stage
	reached bool
	day     model.Date // 实际历法日（预测日）
	days    int        // 相对播种日的序日偏移（含播种日为 0）
}

// walkYear 用某个历史年份把 firstDay..horizon 试走一遍，返回五个阶段的
// 达到情况。共享段累计 cumAtAsOf 作为种子；阈值判定与 cum.Stages 同一
// 口径（累计+1e-9 >= 阈值）。
func (pw *preparedWalk) walkYear(startYear int) []crossing {
	v := pw.wc.variety
	total := pw.cumAtAsOf
	out := make([]crossing, len(model.StageOrder))
	for i, st := range model.StageOrder {
		out[i] = crossing{stage: st}
	}
	offset := dayOffset(pw.wc.plot.SowDate, pw.wc.firstDay)
	for d := pw.wc.firstDay; !d.After(pw.wc.horizon); d = d.AddDate(0, 0, 1) {
		g := 0.0
		b := activeBindingAt(pw.wc.bindings, d)
		if b != nil {
			if _, ok := pw.wc.stations[b.StationCode]; ok {
				if tmax, tmin, have := pw.memberTemp(b.StationCode, d, startYear); have {
					g = gdd.Daily(pw.wc.method, tmax, tmin, v.BaseTemp, v.UpperTemp)
				}
			}
		}
		total += g
		for i, st := range model.StageOrder {
			thr := v.Thresholds[i]
			if !out[i].reached && total+1e-9 >= thr {
				out[i] = crossing{stage: st, reached: true, day: d, days: offset}
			}
		}
		offset++
	}
	return out
}

// stageReachedDate 在共享的已发生段上判定阶段实际达到日；返回 nil 表示
// asOf 收盘时还没达到。
func (pw *preparedWalk) stageReachedDate(thr float64) *model.Date {
	if pw.cumAtAsOf+1e-9 < thr {
		return nil
	}
	total := 0.0
	for _, day := range pw.pastDays {
		total += day.GDD
		if total+1e-9 >= thr {
			d := day.Date
			return &d
		}
	}
	return nil
}

// distributions 在全部成员年上试走，按阶段汇总分布与各阶段阈值/实际日。
type stageWalk struct {
	stage     model.Stage
	threshold float64
	actual    *model.Date // asOf 前已达到的实际日（nil 表示未达到）
	dist      outlook.Distribution
}

func (pw *preparedWalk) distributions() ([]stageWalk, error) {
	v := pw.wc.variety
	perStage := make(map[model.Stage][]outlook.Member, len(model.StageOrder))
	for _, y := range pw.years {
		cs := pw.walkYear(y)
		for _, c := range cs {
			m := outlook.Member{Year: y, Reached: c.reached, Days: c.days}
			perStage[c.stage] = append(perStage[c.stage], m)
		}
	}
	out := make([]stageWalk, 0, len(model.StageOrder))
	for _, st := range model.StageOrder {
		thr, err := stageThreshold(v, st)
		if err != nil {
			return nil, err
		}
		out = append(out, stageWalk{
			stage:     st,
			threshold: thr,
			actual:    pw.stageReachedDate(thr),
			dist:      outlook.NewDistribution(perStage[st]),
		})
	}
	return out, nil
}

// offsetToDate 把相对播种日的序日偏移换回历法日。
func (pw *preparedWalk) offsetToDate(days int) model.Date {
	return pw.wc.plot.SowDate.AddDate(0, 0, days)
}

// buildStageOutlook 组装单个阶段的对外结果。已达到阶段范围收成实际日。
func (pw *preparedWalk) buildStageOutlook(sw stageWalk, qs []float64) (model.StageOutlook, error) {
	so := model.StageOutlook{
		Stage: sw.stage, Threshold: sw.threshold, Quantiles: []model.Quantile{},
	}
	if sw.actual != nil {
		so.Status = model.StatusReached
		d := *sw.actual
		so.ActualDate = &d
		so.Earliest, so.Latest = &d, &d
		for _, q := range qs {
			dd := d
			so.Quantiles = append(so.Quantiles, model.Quantile{Q: q, Reachable: true, Date: &dd})
		}
		return so, nil
	}
	so.Status = model.StatusForecast
	so.NYears = sw.dist.N()
	so.UnreachedYears = sw.dist.Unreached()
	if e, ok := sw.dist.Earliest(); ok {
		d := pw.offsetToDate(e)
		so.Earliest = &d
	}
	if l, ok := sw.dist.Latest(); ok {
		d := pw.offsetToDate(l)
		so.Latest = &d
	}
	for _, q := range qs {
		days, reachable, err := sw.dist.Quantile(q)
		if err != nil {
			return model.StageOutlook{}, err
		}
		qq := model.Quantile{Q: q, Reachable: reachable}
		if reachable {
			d := pw.offsetToDate(days)
			qq.Date = &d
		}
		so.Quantiles = append(so.Quantiles, qq)
	}
	return so, nil
}

// loadWalkForQuery 解析查询日期并加载试走上下文；plot 不存在返回 notFound。
func (s *Service[T]) loadWalkForQuery(tx T, plotCode, queryDate string) (*preparedWalk, error) {
	p, err := tx.GetPlot(plotCode)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, notFoundError{what: "地块", code: plotCode}
	}
	q := s.nowDate()
	if queryDate != "" {
		qd, err := time.Parse("2006-01-02", queryDate)
		if err != nil {
			return nil, fmt.Errorf("查询日期格式应为 YYYY-MM-DD：%q", queryDate)
		}
		q = qd.UTC()
	}
	if err := ValidateSowQuery(p.SowDate, q); err != nil {
		return nil, err
	}
	return s.prepareWalk(tx, p, q)
}

// PlotOutlook 返回地块五个未达到阶段的历年试走范围。
// quantiles 为空时用默认 [0.1,0.5,0.9]；必须全部落在 [0,1]。
func (s *Service[T]) PlotOutlook(ctx context.Context, plotCode, queryDate string,
	quantiles []float64) (*model.Outlook, error) {
	qs := quantiles
	if len(qs) == 0 {
		qs = DefaultQuantiles
	}
	for _, q := range qs {
		if q < 0 || q > 1 || q != q { // NaN 自检
			return nil, fmt.Errorf("分位必须在 0 到 1 之间，收到 %g", q)
		}
	}

	out := &model.Outlook{}
	err := s.store.View(ctx, func(tx T) error {
		pw, err := s.loadWalkForQuery(tx, plotCode, queryDate)
		if err != nil {
			return err
		}
		walks, err := pw.distributions()
		if err != nil {
			return err
		}
		// 若还有未达到阶段却没有任何历年可试走，才明确报错；五阶段都
		// 已达到时（范围收成实际日）不需要历年资料，照常回答。
		hasForecast := false
		for _, sw := range walks {
			if sw.actual == nil {
				hasForecast = true
			}
		}
		if hasForecast && (pw.firstStation == nil || len(pw.years) == 0) {
			stn := ""
			if pw.firstStation != nil {
				stn = pw.firstStation.Code
			}
			return noHistoryError{plot: plotCode, station: stn}
		}
		out.PlotCode = plotCode
		out.AsOf = pw.wc.asOf
		out.Horizon = pw.wc.horizon
		out.Stages = make([]model.StageOutlook, 0, len(walks))
		for _, sw := range walks {
			so, err := pw.buildStageOutlook(sw, qs)
			if err != nil {
				return err
			}
			out.Stages = append(out.Stages, so)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// StageProbabilityQuery 入参：阶段、“某天之前”的日期。
type StageProbabilityQuery struct {
	PlotCode  string
	Stage     string
	By        string // YYYY-MM-DD
	QueryDate string // 可选 asOf
}

// PlotStageProbability 回答“多大概率在 by 当天或之前达到该阶段”。
// 已达到阶段：by 在实际日之前比例 0、当天起 1。
// 未达到阶段：用成员年里达到日不晚于 by 的年数 / 参与年数；窗口外日期
// 仍按窗口内达到的年数计（没到的年就是没到，不会随日期外推而变多）。
func (s *Service[T]) PlotStageProbability(ctx context.Context, in StageProbabilityQuery) (*model.StageProbability, error) {
	st, err := parseStageName(in.Stage)
	if err != nil {
		return nil, err
	}
	by, err := time.Parse("2006-01-02", in.By)
	if err != nil {
		return nil, fmt.Errorf("日期格式应为 YYYY-MM-DD：%q", in.By)
	}
	by = by.UTC()

	res := &model.StageProbability{Stage: st}
	err = s.store.View(ctx, func(tx T) error {
		pw, err := s.loadWalkForQuery(tx, in.PlotCode, in.QueryDate)
		if err != nil {
			return err
		}
		if by.Before(pw.wc.plot.SowDate) {
			return fmt.Errorf("比例查询日期 %s 早于播种日 %s",
				by.Format("2006-01-02"), pw.wc.plot.SowDate.Format("2006-01-02"))
		}
		thr, err := stageThreshold(pw.wc.variety, st)
		if err != nil {
			return err
		}
		res.PlotCode = in.PlotCode
		res.Threshold = thr
		res.AsOf = pw.wc.asOf
		res.By = by

		// 已达到阶段：与历年资料无关。
		if actual := pw.stageReachedDate(thr); actual != nil {
			res.Status = model.StatusReached
			d := *actual
			res.ActualDate = &d
			if !by.Before(*actual) {
				res.ReachedBy = 1
				res.Proportion = 1
			}
			return nil
		}

		res.Status = model.StatusForecast
		if pw.firstStation == nil || len(pw.years) == 0 {
			return noHistoryError{plot: in.PlotCode,
				station: stationCode(pw.firstStation)}
		}
		walks, err := pw.distributions()
		if err != nil {
			return err
		}
		var sw stageWalk
		for _, w := range walks {
			if w.stage == st {
				sw = w
			}
		}
		n := sw.dist.N()
		byDays := dayOffset(pw.wc.plot.SowDate, by)
		k := sw.dist.ReachedBy(byDays)
		res.NYears = n
		res.ReachedBy = k
		if n > 0 {
			res.Proportion = float64(k) / float64(n)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func stationCode(st *model.Station) string {
	if st == nil {
		return ""
	}
	return st.Code
}
