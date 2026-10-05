package engine

import (
	"context"
	"math"
	"testing"
	"time"

	"agristation/internal/model"
)

// setupRangePlot 建立：站 S1、品种 ZZ（阈值 thresholds）、3/1 播种的 mean
// 口径地块 P1，并把服务时钟固定在 asOf。fn 给全年气候平均。
func setupRangePlotAt(t *testing.T, thresholds []float64, asOf time.Time) *testSvc {
	t.Helper()
	s, _ := newTestService()
	s.SetClock(func() time.Time { return asOf })
	seedBase(t, s)
	must(t, s.RegisterVariety(context.Background(), model.Variety{
		Code: "ZZ", Name: "范围品种", BaseTemp: 10, UpperTemp: 30,
		Thresholds: thresholds,
	}))
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.RegisterPlot(context.Background(), model.Plot{
		Code: "P1", Name: "P1号地", SowDate: sow, Variety: "ZZ", Method: "mean",
	}, "S1"); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestRangesEqualPointForecast 把每个参与年份的历史都设成与气候平均逐日相同，
// 则各分位、最早、最晚都必须等于原有的单点预计日期。
func TestRangesEqualPointForecast(t *testing.T) {
	ctx := context.Background()
	// asOf 取播种日当天：五阶段全部未达到，窗口 270 天不跨年，
	// 候选年只需覆盖本日历年内 3..11 月。
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	thresholds := []float64{100, 400, 900, 1500, 1600}
	s := setupRangePlotAt(t, thresholds, asOf)

	fn := func(doy int) (float64, float64) {
		// 随日序缓慢变化的“正常年”，避免恒温退化掩盖错位。
		tmax := 20 + 3*math.Sin(float64(doy)*0.03)
		tmin := tmax - 8
		return tmax, tmin
	}
	seedNormalsAll(t, s, "S1", fn)
	for y := 2000; y <= 2006; y++ {
		yy := y
		seedHistoricalYear(t, s, "S1", yy, func(d time.Time) (float64, float64) {
			return fn(d.YearDay())
		})
	}

	pointStages := stagesOf(t, s, "P1")
	rngRes, err := s.StageRanges(ctx, "P1", nil, "")
	must(t, err)
	if !rngRes.Available || rngRes.Reason != "" {
		t.Fatalf("应有可用范围：available=%v reason=%s", rngRes.Available, rngRes.Reason)
	}
	if len(rngRes.Ranges) != 5 {
		t.Fatalf("应返回 5 个阶段，got %d", len(rngRes.Ranges))
	}
	for i, r := range rngRes.Ranges {
		if r.YearsUsed != 7 {
			t.Fatalf("阶段 %s 应用 7 年，got %d", r.Stage, r.YearsUsed)
		}
		pt := pointStages[i]
		if pt.Date == nil {
			t.Fatalf("单点预测阶段 %s 无日期，无法对比", r.Stage)
		}
		if r.Reached {
			if !r.ActualDate.Equal(*pt.Date) {
				t.Fatalf("已达到阶段 %s 实际日不一致：%s vs %s",
					r.Stage, r.ActualDate.Format("01-02"), pt.Date.Format("01-02"))
			}
			continue
		}
		if r.NotReachedYears != 0 {
			t.Fatalf("与正常年相同时不应有窗口内未到年：阶段 %s 未到 %d",
				r.Stage, r.NotReachedYears)
		}
		if r.Earliest == nil || !r.Earliest.Equal(*pt.Date) {
			t.Fatalf("阶段 %s 最早日 %v 应等于单点 %s",
				r.Stage, r.Earliest, pt.Date.Format("01-02"))
		}
		if r.Latest == nil || !r.Latest.Equal(*pt.Date) {
			t.Fatalf("阶段 %s 最晚日 %v 应等于单点 %s",
				r.Stage, r.Latest, pt.Date.Format("01-02"))
		}
		for _, qp := range r.Quantiles {
			if !qp.Reachable || qp.Date == nil || !qp.Date.Equal(*pt.Date) {
				t.Fatalf("阶段 %s 分位 %g 应等于单点 %s：reachable=%v date=%v",
					r.Stage, qp.Quantile, pt.Date.Format("01-02"), qp.Reachable, qp.Date)
			}
		}
	}

	// 比例查询：单点日期当天为 1（所有年同日到达），前一天 <1。
	for i, r := range rngRes.Ranges {
		if r.Reached {
			continue
		}
		d := *pointStages[i].Date
		at, err := s.ArrivalBy(ctx, "P1", r.Stage, d.Format("2006-01-02"), "")
		must(t, err)
		if math.Abs(at.Fraction-1) > 1e-12 {
			t.Fatalf("阶段 %s 在单点日当天比例应为 1，got %g", r.Stage, at.Fraction)
		}
		before, err := s.ArrivalBy(ctx, "P1", r.Stage,
			d.AddDate(0, 0, -1).Format("2006-01-02"), "")
		must(t, err)
		if before.Fraction >= 1 {
			t.Fatalf("阶段 %s 单点日前一天比例应 <1，got %g", r.Stage, before.Fraction)
		}
	}
}

// TestRangesMonotonicQuantilesAndStages 分位从小到大日期不减；同一分位上
// 出苗→成熟日期不减；比例随查询日期不减。
func TestRangesMonotonicQuantilesAndStages(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	thresholds := []float64{100, 300, 600, 900, 1500}
	s := setupRangePlotAt(t, thresholds, asOf)
	seedNormalsAll(t, s, "S1", func(int) (float64, float64) { return 24, 16 }) // GDD 10
	// 10 个冷暖不同的年（mean GDD/天 6..15），最慢年 1500/6=250 天仍在窗口内。
	for y := 2000; y <= 2009; y++ {
		g := 6.0 + float64(y-2000)
		tmax, tmin := 10+g+6, 10+g-6
		yy := y
		seedHistoricalYear(t, s, "S1", yy, func(time.Time) (float64, float64) { return tmax, tmin })
	}
	rngRes, err := s.StageRanges(ctx, "P1", []float64{0.1, 0.25, 0.5, 0.75, 0.9}, "")
	must(t, err)
	if !rngRes.Available {
		t.Fatalf("应可用：%s", rngRes.Reason)
	}
	for _, r := range rngRes.Ranges {
		if r.YearsUsed != 10 {
			t.Fatalf("阶段 %s 应用 10 年，got %d", r.Stage, r.YearsUsed)
		}
		if r.Reached || r.NotReachedYears != 0 {
			t.Fatalf("本用例全部未达到且全部到达，阶段 %s 异常", r.Stage)
		}
		var prev *time.Time
		for _, qp := range r.Quantiles {
			if !qp.Reachable || qp.Date == nil {
				t.Fatalf("本用例分位都应可达：阶段 %s 分位 %g", r.Stage, qp.Quantile)
			}
			if prev != nil && qp.Date.Before(*prev) {
				t.Fatalf("阶段 %s 分位日期回退：%s -> %s",
					r.Stage, prev.Format("01-02"), qp.Date.Format("01-02"))
			}
			prev = qp.Date
		}
	}
	// 同一分位跨阶段不减。
	for _, q := range []float64{0.1, 0.5, 0.9} {
		var prev *time.Time
		for _, r := range rngRes.Ranges {
			var d *time.Time
			if r.Reached {
				d = r.ActualDate
			} else {
				for _, qp := range r.Quantiles {
					if math.Abs(qp.Quantile-q) < 1e-12 {
						d = qp.Date
					}
				}
			}
			if d == nil {
				t.Fatalf("阶段 %s 分位 %g 无日期", r.Stage, q)
			}
			if prev != nil && d.Before(*prev) {
				t.Fatalf("分位 %g 跨阶段日期回退：%s -> %s（%s）",
					q, prev.Format("01-02"), d.Format("01-02"), r.Stage)
			}
			prev = d
		}
	}
	// 比例随日期不减。
	var prevF float64
	for off := 0; off <= 260; off++ {
		d := asOf.AddDate(0, 0, off)
		ar, err := s.ArrivalBy(ctx, "P1", model.StageTasseling, d.Format("2006-01-02"), "")
		must(t, err)
		if ar.Fraction < prevF-1e-12 {
			t.Fatalf("比例随日期回退：off=%d %g < %g", off, ar.Fraction, prevF)
		}
		if ar.Fraction < 0 || ar.Fraction > 1+1e-12 {
			t.Fatalf("比例越界：%g", ar.Fraction)
		}
		prevF = ar.Fraction
	}
	if math.Abs(prevF-1) > 1e-12 {
		t.Fatalf("窗口内最晚日比例应收敛到 1，got %g", prevF)
	}
}

// TestRangesNotReachedYearsAndUnreachableQuantile 部分年份在窗口内到不了：
// 不丢弃、计入数量；落在它们身上的分位如实报“到不了”；最晚日只统计到的；
// 比例与分位同口径。
func TestRangesNotReachedYearsAndUnreachableQuantile(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	thresholds := []float64{50, 200, 500, 700, 3000}
	s := setupRangePlotAt(t, thresholds, asOf)
	seedNormalsAll(t, s, "S1", func(int) (float64, float64) { return 22, 14 })
	// 10 年：前 7 年暖（mean=30，GDD 20/天，成熟需 150 天），后 3 年冷
	//（mean=14，GDD 4/天，270 天仅 1080，永远到不了 3000）。
	for y := 2000; y <= 2009; y++ {
		var tmax, tmin float64
		if y <= 2006 {
			tmax, tmin = 36, 24
		} else {
			tmax, tmin = 18, 10
		}
		yy := y
		seedHistoricalYear(t, s, "S1", yy, func(time.Time) (float64, float64) { return tmax, tmin })
	}
	rngRes, err := s.StageRanges(ctx, "P1", []float64{0.1, 0.5, 0.9}, "")
	must(t, err)
	var mat model.StageRange
	for _, r := range rngRes.Ranges {
		if r.Stage == model.StageMaturity {
			mat = r
		}
	}
	if mat.YearsUsed != 10 {
		t.Fatalf("应用 10 年，got %d", mat.YearsUsed)
	}
	if mat.NotReachedYears != 3 {
		t.Fatalf("应有 3 个未到年，got %d", mat.NotReachedYears)
	}
	if mat.Latest == nil {
		t.Fatalf("最晚日应统计 7 个到达年")
	}
	var q10, q50, q90 model.QuantilePoint
	for _, qp := range mat.Quantiles {
		switch {
		case math.Abs(qp.Quantile-0.1) < 1e-12:
			q10 = qp
		case math.Abs(qp.Quantile-0.5) < 1e-12:
			q50 = qp
		case math.Abs(qp.Quantile-0.9) < 1e-12:
			q90 = qp
		}
	}
	if !q10.Reachable || !q50.Reachable {
		t.Fatalf("0.1/0.5 分位应可达")
	}
	if q90.Reachable {
		t.Fatalf("0.9 分位落在未到年上，必须报窗口内到不了")
	}
	if q10.Date == nil || q50.Date == nil || q50.Date.Before(*q10.Date) {
		t.Fatalf("分位日期应不减")
	}
	// 7 个暖年同日成熟 → 最早最晚相同。
	if !mat.Earliest.Equal(*mat.Latest) {
		t.Fatalf("暖年全部同日成熟，最早最晚应相同：%s vs %s",
			mat.Earliest.Format("01-02"), mat.Latest.Format("01-02"))
	}
	// 比例：成熟日当天 7/10=0.7；窗口末日仍 0.7。
	fr, err := s.ArrivalBy(ctx, "P1", model.StageMaturity, mat.Earliest.Format("2006-01-02"), "")
	must(t, err)
	if math.Abs(fr.Fraction-0.7) > 1e-12 {
		t.Fatalf("成熟日比例应为 0.7，got %g", fr.Fraction)
	}
	late, err := s.ArrivalBy(ctx, "P1", model.StageMaturity,
		rngRes.Horizon.Format("2006-01-02"), "")
	must(t, err)
	if math.Abs(late.Fraction-0.7) > 1e-12 {
		t.Fatalf("窗口末日比例应仍为 0.7，got %g", late.Fraction)
	}
}

// TestRangesBaseMonotonicity 基点调高：任何可达分位日期只推迟或不变。
func TestRangesBaseMonotonicity(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	thresholds := []float64{100, 300, 600, 900, 1400}
	s := setupRangePlotAt(t, thresholds, asOf)
	seedNormalsAll(t, s, "S1", func(int) (float64, float64) { return 24, 16 })
	for y := 2000; y <= 2006; y++ {
		g := 7.0 + float64(y-2000)*1.5
		tmax, tmin := 10+g+5, 10+g-5
		yy := y
		seedHistoricalYear(t, s, "S1", yy, func(time.Time) (float64, float64) { return tmax, tmin })
	}
	prev := map[model.Stage]map[float64]*time.Time{}
	for base := 8.0; base <= 16.0; base += 2.0 {
		must(t, s.RegisterVariety(ctx, model.Variety{
			Code: "ZZ", BaseTemp: base, UpperTemp: 30, Thresholds: thresholds,
		}))
		res, err := s.StageRanges(ctx, "P1", []float64{0.1, 0.5, 0.9}, "")
		must(t, err)
		for _, r := range res.Ranges {
			if r.Reached {
				continue
			}
			for _, qp := range r.Quantiles {
				if !qp.Reachable {
					continue
				}
				if old := prev[r.Stage][qp.Quantile]; old != nil && qp.Date.Before(*old) {
					t.Fatalf("基点升到 %g 后阶段 %s 分位 %g 提前：%s -> %s",
						base, r.Stage, qp.Quantile, old.Format("01-02"), qp.Date.Format("01-02"))
				}
				if prev[r.Stage] == nil {
					prev[r.Stage] = map[float64]*time.Time{}
				}
				d := *qp.Date
				prev[r.Stage][qp.Quantile] = &d
			}
		}
	}
}

// TestRangesReachedUnaffectedAndCollapsed 已达到阶段不受历年资料影响，
// 范围收成实际日、分位同日、比例 0/1 跳变。
func TestRangesReachedUnaffectedAndCollapsed(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC) // 窗口到 12/27，不跨年
	thresholds := []float64{30, 200, 5000, 6000, 9000}
	s := setupRangePlotAt(t, thresholds, asOf)
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedNormalsAll(t, s, "S1", func(int) (float64, float64) { return 25, 15 })
	// 观测 3/1..3/31 共 31 天 *GDD 10 = 310：出苗（30）3/3、拔节（200）3/20 达到。
	batch := make([]ObservationInput, 0, 31)
	for i := 0; i < 31; i++ {
		batch = append(batch, obs("S1", dstr(sow.AddDate(0, 0, i)), 25, 15, 1))
	}
	_, err := s.IngestObservations(ctx, batch)
	must(t, err)

	before, err := s.StageRanges(ctx, "P1", nil, "")
	must(t, err)
	if !before.Available {
		t.Fatalf("已有两个阶段达到，顶层应可用：%s", before.Reason)
	}
	var joint model.StageRange
	for _, r := range before.Ranges {
		if r.Stage == model.StageJointing {
			joint = r
			if !r.Reached || r.ActualDate == nil {
				t.Fatalf("拔节应已达到")
			}
		}
	}

	// 导入极端热的历年：已达到阶段必须纹丝不动。
	for y := 2000; y <= 2005; y++ {
		yy := y
		seedHistoricalYear(t, s, "S1", yy, func(time.Time) (float64, float64) { return 45, 40 })
	}
	after, err := s.StageRanges(ctx, "P1", nil, "")
	must(t, err)
	for _, r := range after.Ranges {
		if r.Stage != model.StageEmergence && r.Stage != model.StageJointing {
			continue
		}
		if !r.Reached || r.Earliest == nil || r.Latest == nil {
			t.Fatalf("阶段 %s 应 reached 且有单日范围", r.Stage)
		}
		if !r.Earliest.Equal(*r.Latest) || !r.Earliest.Equal(*r.ActualDate) {
			t.Fatalf("已达到阶段应收成实际日一天")
		}
		for _, qp := range r.Quantiles {
			if !qp.Reachable || !qp.Date.Equal(*r.ActualDate) {
				t.Fatalf("已达到阶段分位应等于实际日")
			}
		}
	}
	// 比例：实际日前 0、当天起 1。
	d := *joint.ActualDate
	at, err := s.ArrivalBy(ctx, "P1", model.StageJointing, d.Format("2006-01-02"), "")
	must(t, err)
	if at.Fraction != 1 || !at.Reached {
		t.Fatalf("已达到阶段当天比例应为 1/reached，got %g", at.Fraction)
	}
	prior, err := s.ArrivalBy(ctx, "P1", model.StageJointing,
		d.AddDate(0, 0, -1).Format("2006-01-02"), "")
	must(t, err)
	if prior.Fraction != 0 {
		t.Fatalf("已达到阶段之前比例应为 0，got %g", prior.Fraction)
	}
}

// TestRangesNoHistoricalAvailable 一块地连一年可用历年都没有时，接口明确
// 说给不出范围，不回看着正常的空结果。
func TestRangesNoHistoricalAvailable(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	thresholds := []float64{100, 400, 1200, 1500, 3000}
	s := setupRangePlotAt(t, thresholds, asOf)
	seedNormalsAll(t, s, "S1", func(int) (float64, float64) { return 25, 15 })

	res, err := s.StageRanges(ctx, "P1", nil, "")
	must(t, err)
	if res.Available {
		t.Fatalf("无任何历年资料时顶层不应标记可用")
	}
	if res.Reason == "" {
		t.Fatalf("必须给出给不出范围的原因")
	}
	for _, r := range res.Ranges {
		if r.Available || r.Reached {
			t.Fatalf("未达到阶段在无资料时不可用：%+v", r)
		}
		if len(r.Quantiles) != 0 || r.YearsUsed != 0 {
			t.Fatalf("不可用阶段不应带分位/年数")
		}
	}

	ar, err := s.ArrivalBy(ctx, "P1", model.StageTasseling, "2026-08-01", "")
	must(t, err)
	if ar.Available {
		t.Fatalf("无历年时比例查询也应明确不可用")
	}
	if ar.Reason == "" {
		t.Fatalf("不可用必须带原因")
	}
}

// TestRangesInvalidArguments 非法查询拒绝：分位越界、阶段名不存在、
// 比例查询日期早于播种日。
func TestRangesInvalidArguments(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	thresholds := []float64{100, 400, 1200, 1500, 3000}
	s := setupRangePlotAt(t, thresholds, asOf)
	seedNormalsAll(t, s, "S1", func(int) (float64, float64) { return 25, 15 })
	seedHistoricalYear(t, s, "S1", 2005, func(time.Time) (float64, float64) { return 25, 15 })

	if _, err := s.StageRanges(ctx, "P1", []float64{0.5, 1.01}, ""); err == nil {
		t.Fatal("分位 >1 应拒绝")
	}
	if _, err := s.StageRanges(ctx, "P1", []float64{-0.01}, ""); err == nil {
		t.Fatal("分位 <0 应拒绝")
	}
	if _, err := s.StageRanges(ctx, "NOPE", nil, ""); err == nil {
		t.Fatal("不存在地块应报错")
	}
	if _, err := s.ArrivalBy(ctx, "P1", "flowering", "2026-08-01", ""); err == nil {
		t.Fatal("不存在阶段名应拒绝")
	}
	if _, err := s.ArrivalBy(ctx, "P1", model.StageTasseling, "2026-02-01", ""); err == nil {
		t.Fatal("早于播种日的比例查询应拒绝")
	}
	if _, err := s.ArrivalBy(ctx, "P1", model.StageTasseling, "08-01", ""); err == nil {
		t.Fatal("错误日期格式应拒绝")
	}
	if _, err := s.StageRanges(ctx, "P1", nil, "bad-date"); err == nil {
		t.Fatal("错误查询日期格式应拒绝")
	}
}
