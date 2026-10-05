package engine

import (
	"context"
	"fmt"
	"math"
	"sort"
	"testing"
	"time"

	"agristation/internal/model"
)

// histImport 导入一整年（默认 365 天，闰年 366）逐日固定气温。
func histImport(t *testing.T, s *testSvc, station string, year int, tmax, tmin float64) {
	t.Helper()
	d0 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	days := 365
	if time.Date(year, 2, 29, 0, 0, 0, 0, time.UTC).Day() == 29 {
		days = 366
	}
	recs := make([]HistoricalInput, 0, days)
	for i := 0; i < days; i++ {
		d := d0.AddDate(0, 0, i)
		recs = append(recs, HistoricalInput{
			StationCode: station, Year: year, Date: d.Format("2006-01-02"),
			TMax: tmax, TMin: tmin,
		})
	}
	res, err := s.ImportHistorical(context.Background(), recs)
	must(t, err)
	for _, it := range res.Items {
		if !it.OK || !it.Applied {
			t.Fatalf("站 %s 年 %d 第 %d 条应整体生效：%+v", station, year, it.Index, it)
		}
	}
}

// TestHistoricalImportValidation 逐条非法回报；一站一年整体生效或整体不生效。
func TestHistoricalImportValidation(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)

	res, err := s.ImportHistorical(ctx, []HistoricalInput{
		{StationCode: "S1", Year: 2010, Date: "2010-06-01", TMax: 25, TMin: 15}, // 合法
		{StationCode: "S1", Year: 2010, Date: "2010-06-02", TMax: 10, TMin: 20}, // 最低>最高，同站同年
		{StationCode: "S2", Year: 2011, Date: "2011-06-01", TMax: 25, TMin: 15}, // 另一站年：合法
		{StationCode: "S2", Year: 2011, Date: "bad-date", TMax: 25, TMin: 15},   // 日期格式错，连坐 S2/2011
		{StationCode: "NOPE", Year: 2012, Date: "2012-06-01", TMax: 25, TMin: 15},
		{StationCode: "S1", Year: 2013, Date: "2014-01-01", TMax: 25, TMin: 15},  // 日期不属于所标年份
		{StationCode: "S1", Year: 2014, Date: "2014-01-01", TMax: 200, TMin: 10}, // 越界
	})
	must(t, err)
	byIdx := map[int]HistoricalItemResult{}
	for _, it := range res.Items {
		byIdx[it.Index] = it
	}
	for i, it := range byIdx {
		if i != 2 {
			if it.OK {
				t.Fatalf("第 %d 条应失败（站-年整体不生效）：%+v", i, it)
			}
			if it.Reason == "" {
				t.Fatalf("第 %d 条应有原因", i)
			}
		}
	}
	// 仅 S2/2011 的合法条与非法条同组：也整体不生效。
	if byIdx[2].OK {
		t.Fatalf("同站同年有非法记录，合法条应被连坐拒收：%+v", byIdx[2])
	}
	// 年结果汇总。
	yearApplied := map[string]bool{}
	for _, y := range res.Years {
		yearApplied[fmt.Sprintf("%s/%d", y.StationCode, y.Year)] = y.Applied
	}
	if len(yearApplied) != 5 {
		t.Fatalf("应有 5 个站-年组，got %d：%+v", len(yearApplied), res.Years)
	}
	for k, applied := range yearApplied {
		if applied {
			t.Fatalf("本组全部站-年都应未生效，%s 却生效", k)
		}
	}
}

// TestHistoricalWholeYearReplace 同站同年再导一次整年替换；其他年不受影响。
func TestHistoricalWholeYearReplace(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	histImport(t, s, "S1", 2010, 25, 15)
	histImport(t, s, "S1", 2011, 26, 16)

	// 整年替换 2010：内容改成只含两天（合法，缺日试走时走气候平均）。
	repl := []HistoricalInput{
		{StationCode: "S1", Year: 2010, Date: "2010-01-01", TMax: 30, TMin: 20},
		{StationCode: "S1", Year: 2010, Date: "2010-07-01", TMax: 30, TMin: 20},
	}
	res, err := s.ImportHistorical(ctx, repl)
	must(t, err)
	var yr HistoricalYearResult
	for _, y := range res.Years {
		if y.StationCode == "S1" && y.Year == 2010 {
			yr = y
		}
	}
	if !yr.Applied || !yr.Replaced || yr.Days != 2 {
		t.Fatalf("2010 应整年替换为 2 天：%+v", yr)
	}

	// 直接核对底层：2010 只剩 2 行，2011 仍是 365 行。
	rows, err := histRowsForTest(s, "S1")
	must(t, err)
	counts := map[int]int{}
	for _, r := range rows {
		counts[r.Year]++
	}
	if counts[2010] != 2 || counts[2011] != 365 {
		t.Fatalf("整年替换后行数错：%+v", counts)
	}
}

// TestHistoricalBatchDedup 同批 (站,日) 重复：折叠为最后一条。
func TestHistoricalBatchDedup(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	res, err := s.ImportHistorical(ctx, []HistoricalInput{
		{StationCode: "S1", Year: 2010, Date: "2010-01-01", TMax: 20, TMin: 10},
		{StationCode: "S1", Year: 2010, Date: "2010-01-01", TMax: 22, TMin: 12},
		{StationCode: "S1", Year: 2010, Date: "2010-01-02", TMax: 22, TMin: 12},
	})
	must(t, err)
	if !res.Items[0].OK || res.Items[0].Applied {
		t.Fatalf("重复组首条应 OK 但不生效：%+v", res.Items[0])
	}
	if !res.Items[1].Applied || !res.Items[2].Applied {
		t.Fatalf("后两条应生效：%+v", res.Items)
	}
	rows, err := histRowsForTest(s, "S1")
	must(t, err)
	if len(rows) != 2 {
		t.Fatalf("去重后应 2 行，got %d", len(rows))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Date.Before(rows[j].Date) })
	if rows[0].TMax != 22 {
		t.Fatalf("应保留最后一条的 22，got %g", rows[0].TMax)
	}
}

// TestNoHistoryClearError 没有任何历年资料时，接口明确报错而不是空结果。
func TestNoHistoryClearError(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	_, err := s.PlotOutlook(ctx, "P1", "", nil)
	if err == nil || !AsNoHistory(err) {
		t.Fatalf("无历年资料应返回明确错误，got %v", err)
	}
	// 概率查询对未达到阶段同样报错。
	_, err = s.PlotStageProbability(ctx, StageProbabilityQuery{
		PlotCode: "P1", Stage: "maturity", By: "2026-09-01"})
	if err == nil || !AsNoHistory(err) {
		t.Fatalf("无历年资料比例查询应报错，got %v", err)
	}
}

// TestOutlookEqualsDeterministicWhenHistIsNormal 核心关系：把某站每一年每
// 一天都设成与它的气候平均相同，各分位日期、最早最晚都等于原单点预计日期。
func TestOutlookEqualsDeterministicWhenHistIsNormal(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "sine", sow)
	// S1 全年气候平均：25/15 恒温（GDD 10/天）。
	for doy := 1; doy <= 366; doy++ {
		must(t, s.PutClimateNormal(ctx, model.ClimateNormal{
			StationCode: "S1", DOY: doy, TMax: 25, TMin: 15}))
	}
	// 观测到 asOf(6/1) 前若干天，之后靠外推。
	batch := make([]ObservationInput, 0)
	for i := 0; i < 40; i++ {
		batch = append(batch, obs("S1", dstr(sow.AddDate(0, 0, i)), 25, 15, 1))
	}
	_, err := s.IngestObservations(ctx, batch)
	must(t, err)

	// 多年历史，每天都等于气候平均（25/15）。
	for _, y := range []int{2001, 2003, 2007, 2011, 2015, 2019, 2021, 2023} {
		histImport(t, s, "S1", y, 25, 15)
	}

	got := stagesOf(t, s, "P1")
	out, err := s.PlotOutlook(ctx, "P1", "", nil)
	must(t, err)
	byStage := map[model.Stage]model.StageDate{}
	for _, st := range got {
		byStage[st.Stage] = st
	}
	for _, so := range out.Stages {
		want := byStage[so.Stage]
		if want.Date == nil {
			// 单点都给不出日期（窗口达不到）：集合也不能给出。
			if so.Earliest != nil || so.Latest != nil {
				t.Fatalf("阶段 %s 单点无日期，集合却给了范围：%+v", so.Stage, so)
			}
			continue
		}
		if so.Earliest == nil || !so.Earliest.Equal(*want.Date) ||
			so.Latest == nil || !so.Latest.Equal(*want.Date) {
			t.Fatalf("阶段 %s 最早/最晚应等于单点预计 %s：earliest=%v latest=%v",
				so.Stage, want.Date.Format("01-02"), so.Earliest, so.Latest)
		}
		for _, q := range so.Quantiles {
			if !q.Reachable || q.Date == nil || !q.Date.Equal(*want.Date) {
				t.Fatalf("阶段 %s 分位 %g 应等于单点预计 %s：%+v",
					so.Stage, q.Q, want.Date.Format("01-02"), q)
			}
		}
	}
}

// TestOutlookMonotonicAndUnreached 分位不减、阶段依次不减、比例不减；
// 没到的年份计入 unreached，分位可如实落在“到不了”上。
func TestOutlookMonotonicAndUnreached(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	// 不设气候平均、不报观测：asOf 前累计为 0，保证五阶段都未达到，
	// 全部进入集合试走（试走年每年 365/366 天资料齐全，不依赖补值）。
	// 10 个年份：8 年偏暖（30/20 -> mean GDD 10/天），2 年极冷
	// （12/10 -> mean=max(11-10,0)=1/天，窗口内多数阶段到不了）。
	for _, y := range []int{2001, 2002, 2003, 2004, 2005, 2006, 2007, 2008} {
		histImport(t, s, "S1", y, 30, 20)
	}
	histImport(t, s, "S1", 2009, 12, 10)
	histImport(t, s, "S1", 2010, 12, 10)

	out, err := s.PlotOutlook(ctx, "P1", "", nil)
	must(t, err)
	if len(out.Stages) != 5 {
		t.Fatalf("应有 5 个阶段，got %d", len(out.Stages))
	}
	for _, so := range out.Stages {
		if so.Status == model.StatusReached {
			// 已达到阶段范围收成实际日，不参与年数口径。
			if so.NYears != 0 || so.UnreachedYears != 0 {
				t.Fatalf("已达到阶段不应报参与/未到年数：%+v", so)
			}
			continue
		}
		if so.NYears != 10 {
			t.Fatalf("阶段 %s 参与年数应为 10，got %d", so.Stage, so.NYears)
		}
		// 分位随 q 不减。
		var prev *time.Time
		for _, q := range so.Quantiles {
			if q.Reachable {
				if q.Date == nil {
					t.Fatalf("reachable 却无日期")
				}
				if prev != nil && q.Date.Before(*prev) {
					t.Fatalf("阶段 %s 分位日期倒退：%s -> %s",
						so.Stage, prev.Format("01-02"), q.Date.Format("01-02"))
				}
				d := *q.Date
				prev = &d
			}
		}
	}
	// 同一分位上：出苗<=拔节<=抽雄<=吐丝<=成熟（日期不减）。
	for _, qv := range []float64{0.1, 0.5} {
		var prev *time.Time
		for _, so := range out.Stages {
			var cur *time.Time
			for _, q := range so.Quantiles {
				if q.Q == qv && q.Reachable {
					cur = q.Date
				}
			}
			if cur != nil && prev != nil && cur.Before(*prev) {
				t.Fatalf("q=%g 阶段顺序被破坏：%s -> %s",
					qv, prev.Format("01-02"), cur.Format("01-02"))
			}
			if cur != nil {
				prev = cur
			}
		}
	}

	// 成熟：冷年在窗口（270 天 *1 = 270，加已发生 0；阈值 800）到不了，
	// unreached=2；q=0.9 排第 9（ceil(9)=9）落在冷年，到不了。
	var mat model.StageOutlook
	for _, so := range out.Stages {
		if so.Stage == model.StageMaturity {
			mat = so
		}
	}
	if mat.UnreachedYears != 2 {
		t.Fatalf("成熟应有 2 年没到，got %d", mat.UnreachedYears)
	}
	for _, q := range mat.Quantiles {
		if q.Q == 0.9 && q.Reachable {
			t.Fatalf("成熟 q=0.9 应如实落在到不了的年份，却给了 %s", q.Date.Format("01-02"))
		}
	}

	// 比例随查询日期不减；冷年永不到 -> 上限 8/10=0.8。
	props := make([]float64, 0)
	for off := 0; off <= 300; off += 3 {
		by := sow.AddDate(0, 0, off)
		pr, err := s.PlotStageProbability(ctx, StageProbabilityQuery{
			PlotCode: "P1", Stage: "maturity", By: by.Format("2006-01-02")})
		must(t, err)
		props = append(props, pr.Proportion)
		if math.Abs(pr.Proportion-float64(pr.ReachedBy)/10) > 1e-12 {
			t.Fatalf("比例与年数不一致：%+v", pr)
		}
	}
	for i := 1; i < len(props); i++ {
		if props[i] < props[i-1] {
			t.Fatalf("比例应随日期不减：%g -> %g", props[i-1], props[i])
		}
	}
	if math.Abs(props[len(props)-1]-0.8) > 1e-9 {
		t.Fatalf("成熟比例上限应为 0.8（8/10），got %g", props[len(props)-1])
	}
}

// TestOutlookReachedStagesCollapse 已达到阶段：范围收成实际日，比例
// 在那天之前 0、当天起 1，且不受历年资料影响。
func TestOutlookReachedStagesCollapse(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	// 前 40 天恒温 20/20 -> GDD 10/天：出苗(30)=3/3、拔节(200)=3/20、
	// 抽雄(400)=4/9 都在 asOf(6/1) 前达到。
	batch := make([]ObservationInput, 0, 40)
	for i := 0; i < 40; i++ {
		batch = append(batch, obs("S1", dstr(sow.AddDate(0, 0, i)), 20, 20, 1))
	}
	_, err := s.IngestObservations(ctx, batch)
	must(t, err)
	// 故意给很离谱的历年资料（极热），已达到阶段也不能受影响。
	histImport(t, s, "S1", 2001, 40, 35)
	histImport(t, s, "S1", 2002, 40, 35)

	out, err := s.PlotOutlook(ctx, "P1", "", nil)
	must(t, err)
	st := stagesOf(t, s, "P1")
	actual := map[model.Stage]*time.Time{}
	for _, x := range st {
		actual[x.Stage] = x.Date
	}
	for _, so := range out.Stages {
		if actual[so.Stage] == nil {
			continue
		}
		want := actual[so.Stage]
		if so.Status != model.StatusReached || so.ActualDate == nil ||
			!so.ActualDate.Equal(*want) {
			t.Fatalf("阶段 %s 应为 reached 实际日 %s：%+v",
				so.Stage, want.Format("01-02"), so)
		}
		if so.Earliest == nil || !so.Earliest.Equal(*want) ||
			so.Latest == nil || !so.Latest.Equal(*want) {
			t.Fatalf("已达到阶段范围应收成实际日：%+v", so)
		}
		if so.UnreachedYears != 0 || so.NYears != 0 {
			t.Fatalf("已达到阶段不应再报未到年/参与年：%+v", so)
		}
	}
	// 比例：抽雄实际 4/9。4/8 -> 0，4/9 -> 1。
	before, err := s.PlotStageProbability(ctx, StageProbabilityQuery{
		PlotCode: "P1", Stage: "tasseling", By: "2026-04-08"})
	must(t, err)
	if before.Proportion != 0 || before.Status != model.StatusReached {
		t.Fatalf("实际日前比例应为 0：%+v", before)
	}
	at, err := s.PlotStageProbability(ctx, StageProbabilityQuery{
		PlotCode: "P1", Stage: "tasseling", By: "2026-04-09"})
	must(t, err)
	if at.Proportion != 1 {
		t.Fatalf("实际日当天起比例应为 1：%+v", at)
	}
}

// TestOutlookBaseRaiseMonotonic 基点调高，任何分位日期只推迟或不变。
func TestOutlookBaseRaiseMonotonic(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "sine", sow)
	// 各年不同温度，避免分位“恒定不变”掩盖单调性问题。
	rng := newSeeded(7)
	for _, y := range []int{2000, 2001, 2002, 2003, 2004, 2005} {
		d0 := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		days := 366
		recs := make([]HistoricalInput, 0, days)
		for i := 0; i < days; i++ {
			d := d0.AddDate(0, 0, i)
			tmin := 8 + rng.Float64()*10
			tmax := tmin + 4 + rng.Float64()*12
			recs = append(recs, HistoricalInput{
				StationCode: "S1", Year: y, Date: d.Format("2006-01-02"),
				TMax: tmax, TMin: tmin,
			})
		}
		_, err := s.ImportHistorical(ctx, recs)
		must(t, err)
	}

	prev := map[model.Stage]map[float64]*time.Time{}
	for _, st := range model.StageOrder {
		prev[st] = map[float64]*time.Time{}
	}
	for base := 5.0; base <= 15.0; base += 2.0 {
		must(t, s.RegisterVariety(ctx, model.Variety{
			Code: "ZD958", BaseTemp: base, UpperTemp: 35,
			Thresholds: []float64{30, 200, 400, 460, 800},
		}))
		out, err := s.PlotOutlook(ctx, "P1", "", nil)
		must(t, err)
		for _, so := range out.Stages {
			if so.Status == model.StatusReached {
				// 基点调高后仍可能 reached：实际日同样不应提前。
				for _, q := range so.Quantiles {
					if p := prev[so.Stage][q.Q]; p != nil && q.Date.Before(*p) {
						t.Fatalf("基点 %g 阶段 %s q=%g 提前：%s -> %s",
							base, so.Stage, q.Q, p.Format("01-02"), q.Date.Format("01-02"))
					}
					if q.Reachable {
						d := *q.Date
						prev[so.Stage][q.Q] = &d
					}
				}
				continue
			}
			for _, q := range so.Quantiles {
				p := prev[so.Stage][q.Q]
				if p != nil && q.Reachable && q.Date.Before(*p) {
					t.Fatalf("基点 %g 阶段 %s q=%g 提前：%s -> %s",
						base, so.Stage, q.Q, p.Format("01-02"), q.Date.Format("01-02"))
				}
				if q.Reachable {
					d := *q.Date
					prev[so.Stage][q.Q] = &d
				}
			}
		}
	}
}

// TestOutlookInvalidQueries 分位越界、阶段不存在、比例日期早于播种日均拒绝。
func TestOutlookInvalidQueries(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	histImport(t, s, "S1", 2010, 25, 15)

	if _, err := s.PlotOutlook(ctx, "P1", "", []float64{0.5, 1.01}); err == nil {
		t.Fatal("分位 >1 应拒绝")
	}
	if _, err := s.PlotOutlook(ctx, "P1", "", []float64{-0.1}); err == nil {
		t.Fatal("分位 <0 应拒绝")
	}
	if _, err := s.PlotOutlook(ctx, "NOPE", "", nil); err == nil {
		t.Fatal("地块不存在应拒绝")
	}
	if _, err := s.PlotStageProbability(ctx, StageProbabilityQuery{
		PlotCode: "P1", Stage: "flowering", By: "2026-08-01"}); err == nil {
		t.Fatal("不存在的阶段名应拒绝")
	}
	if _, err := s.PlotStageProbability(ctx, StageProbabilityQuery{
		PlotCode: "P1", Stage: "tasseling", By: "2026-02-01"}); err == nil {
		t.Fatal("比例查询日期早于播种日应拒绝")
	}
	if _, err := s.PlotStageProbability(ctx, StageProbabilityQuery{
		PlotCode: "P1", Stage: "tasseling", By: "bad"}); err == nil {
		t.Fatal("非法日期应拒绝")
	}
}
