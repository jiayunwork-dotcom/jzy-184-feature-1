package engine

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"

	"agristation/internal/model"
)

// TestExample12ThroughEngine 端到端验证题目算例：某天最高 30 最低 14，
// 基点 10、上限 30，三种口径下该日积温都应为 12。
func TestExample12ThroughEngine(t *testing.T) {
	ctx := context.Background()
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	day := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for _, method := range []string{"sine", "triangle", "mean", ""} {
		s, _ := newTestService()
		seedBase(t, s)
		code := "P-" + method
		if method == "" {
			code = "P-default"
		}
		seedPlot(t, s, code, "S1", method, sow)
		// 其余日子给区间内恒温 20（GDD=10），目标日给 (30,14)。
		batch := []ObservationInput{obs("S1", dstr(day), 30, 14, 1)}
		res, err := s.IngestObservations(ctx, batch)
		must(t, err)
		if len(res.Items) != 1 || !res.Items[0].Applied {
			t.Fatalf("写入应生效：%+v", res.Items)
		}
		rows := dailyOf(t, s, code)
		var found model.DailyValue
		for _, r := range rows {
			if r.Date.Equal(day) {
				found = r
			}
		}
		if found.GDD != 12 {
			t.Fatalf("方法 %q：当日积温应为 12，got %g", method, found.GDD)
		}
	}
}

// TestGDDZeroAndNonNegative 端到端零与非负。
func TestGDDZeroAndNonNegative(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "sine", sow)
	cold := time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC)
	_, err := s.IngestObservations(ctx, []ObservationInput{
		obs("S1", dstr(cold), 9, -20, 1),
	})
	must(t, err)
	for _, r := range dailyOf(t, s, "P1") {
		if r.GDD < 0 {
			t.Fatalf("积温为负：%s %g", r.Date.Format("01-02"), r.GDD)
		}
		if r.Date.Equal(cold) && r.GDD != 0 {
			t.Fatalf("冷日积温应为 0，got %g", r.GDD)
		}
	}
}

// TestBaseMonotonicityStages 基点调高，各阶段日期只推迟或不变。
func TestBaseMonotonicityStages(t *testing.T) {
	ctx := context.Background()
	for iter := 0; iter < 8; iter++ {
		s, _ := newTestService()
		seedBase(t, s)
		sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
		seedPlot(t, s, "P1", "S1", "sine", sow)
		// 给 40 天随机温度。
		rng := rand.New(rand.NewSource(int64(iter + 1)))
		var batch []ObservationInput
		for i := 0; i < 40; i++ {
			day := sow.AddDate(0, 0, i)
			tmin := 5 + rng.Float64()*15
			tmax := tmin + 5 + rng.Float64()*15
			batch = append(batch, obs("S1", dstr(day), tmax, tmin, 1))
		}
		_, err := s.IngestObservations(ctx, batch)
		must(t, err)

		prevDates := map[model.Stage]*time.Time{}
		for base := 5.0; base <= 15.0; base += 1.0 {
			must(t, s.RegisterVariety(ctx, model.Variety{
				Code: "ZD958", BaseTemp: base, UpperTemp: 35,
				Thresholds: []float64{30, 200, 400, 460, 800},
			}))
			for _, st := range stagesOf(t, s, "P1") {
				old := prevDates[st.Stage]
				if old != nil && st.Date != nil && st.Date.Before(*old) {
					t.Fatalf("iter=%d 基点升到 %g 时阶段 %s 日期反而提前：%s -> %s",
						iter, base, st.Stage, old.Format("01-02"), st.Date.Format("01-02"))
				}
				if st.Date != nil {
					d := *st.Date
					prevDates[st.Stage] = &d
				} else {
					prevDates[st.Stage] = nil
				}
			}
		}
	}
}

// TestOutOfOrderAndDuplicate 乱序补传、重复上报、序号仲裁。
func TestOutOfOrderAndDuplicate(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	day := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)

	// 先到序号 3。
	r1, err := s.IngestObservations(ctx, []ObservationInput{obs("S1", dstr(day), 30, 10, 3)})
	must(t, err)
	if !r1.Items[0].Applied {
		t.Fatal("序号 3 应生效")
	}
	// 晚到序号 1：合法但不能覆盖。
	r2, err := s.IngestObservations(ctx, []ObservationInput{obs("S1", dstr(day), 40, 30, 1)})
	must(t, err)
	if r2.Items[0].Applied {
		t.Fatal("小序号晚到记录不应生效")
	}
	rows := dailyOf(t, s, "P1")
	var v model.DailyValue
	for _, r := range rows {
		if r.Date.Equal(day) {
			v = r
		}
	}
	if v.TMax == nil || *v.TMax != 30 {
		t.Fatalf("小序号不应覆盖：tmax=%v", v.TMax)
	}
	// 完全相同的序号 3 重复补传：幂等。
	r3, err := s.IngestObservations(ctx, []ObservationInput{obs("S1", dstr(day), 30, 10, 3)})
	must(t, err)
	if r3.ChangeID != "" {
		t.Fatal("完全重复上报不应产生变更")
	}
	// 更大序号 5（传感器更正）：覆盖并重算。
	r4, err := s.IngestObservations(ctx, []ObservationInput{obs("S1", dstr(day), 24, 12, 5)})
	must(t, err)
	if !r4.Items[0].Applied || r4.ChangeID == "" {
		t.Fatalf("更大序号应生效并触发变更：%+v", r4)
	}
	rows = dailyOf(t, s, "P1")
	for _, r := range rows {
		if r.Date.Equal(day) {
			if r.TMax == nil || *r.TMax != 24 {
				t.Fatalf("更正未生效：%v", r.TMax)
			}
			// mean 口径 (24+12)/2-10 = 8
			if math.Abs(r.GDD-8) > 1e-9 {
				t.Fatalf("更正后 GDD 应为 8，got %g", r.GDD)
			}
		}
	}
}

// TestBatchPartialInvalid 一批里混非法记录：其余照常写入，逐条给原因。
func TestBatchPartialInvalid(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	res, err := s.IngestObservations(ctx, []ObservationInput{
		obs("S1", "2026-04-05", 25, 15, 1),   // 合法
		obs("S1", "2026-04-06", 10, 20, 1),   // 最低>最高
		obs("S1", "2026-04-07", 200, 10, 1),  // 超范围
		obs("S1", "bad-date", 20, 10, 1),     // 日期格式
		obs("NOPE", "2026-04-08", 20, 10, 1), // 站不存在
		obs("S1", "2026-04-09", 20, 10, 0),   // 序号非法
		obs("S2", "2026-04-10", 22, 12, 1),   // 合法
	})
	must(t, err)
	good := 0
	for _, it := range res.Items {
		switch it.Index {
		case 0, 6:
			if !it.OK || !it.Applied {
				t.Fatalf("第 %d 条应写入生效：%+v", it.Index, it)
			}
			good++
		default:
			if it.OK {
				t.Fatalf("第 %d 条应被拒绝：%+v", it.Index, it)
			}
			if it.Reason == "" {
				t.Fatalf("第 %d 条应给出原因", it.Index)
			}
		}
	}
	if good != 2 {
		t.Fatalf("应有 2 条合法写入，got %d", good)
	}
}

// TestFilledThenReplaced 缺测先补值，真实数据到达后自动替换。
func TestFilledThenReplaced(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	day := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	// S1 缺测，S2 同日有观测（同海拔、邻近）。
	_, err := s.IngestObservations(ctx, []ObservationInput{obs("S2", dstr(day), 30, 20, 1)})
	must(t, err)
	rows := dailyOf(t, s, "P1")
	var f model.DailyValue
	for _, r := range rows {
		if r.Date.Equal(day) {
			f = r
		}
	}
	if f.Source != model.SourceFilled || f.FillMethod != model.FillNeighbor ||
		f.FillFrom == nil || *f.FillFrom != "S2" {
		t.Fatalf("应标记为 S2 邻站补值：source=%s method=%s from=%v", f.Source, f.FillMethod, f.FillFrom)
	}
	// S1 真实数据晚到：替换补值。
	_, err = s.IngestObservations(ctx, []ObservationInput{obs("S1", dstr(day), 24, 14, 2)})
	must(t, err)
	rows = dailyOf(t, s, "P1")
	for _, r := range rows {
		if r.Date.Equal(day) {
			if r.Source != model.SourceObserved {
				t.Fatalf("真实数据应替换补值，source=%s", r.Source)
			}
			if r.FillFrom != nil {
				t.Fatalf("替换后不应再有补值来源：%v", r.FillFrom)
			}
			if math.Abs(r.GDD-9) > 1e-9 {
				t.Fatalf("真实值 (24,14) mean 口径应为 9，got %g", r.GDD)
			}
		}
	}
}

// TestRebind 改绑后用新站数据重算，旧站数据不再影响该地块。
func TestRebind(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	from := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	// 改绑前 S1、S2 温度不同。
	batch := []ObservationInput{
		obs("S1", "2026-04-10", 20, 20, 1), // GDD 10
		obs("S2", "2026-04-10", 20, 20, 1),
		obs("S1", "2026-04-12", 20, 20, 1), // GDD 10
		obs("S2", "2026-04-12", 30, 30, 1), // GDD 20
	}
	_, err := s.IngestObservations(ctx, batch)
	must(t, err)
	must(t, s.BindPlot(ctx, BindPlotInput{
		PlotCode: "P1", StationCode: "S2", EffectiveDate: dstr(from),
	}))
	rows := dailyOf(t, s, "P1")
	for _, r := range rows {
		switch {
		case r.Date.Equal(time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)):
			if math.Abs(r.GDD-10) > 1e-9 {
				t.Fatalf("改绑前仍用 S1，GDD 应为 10，got %g", r.GDD)
			}
		case r.Date.Equal(time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)):
			if math.Abs(r.GDD-20) > 1e-9 {
				t.Fatalf("改绑后应用 S2，GDD 应为 20，got %g", r.GDD)
			}
		}
	}
	// 改绑到不存在的站应报错。
	if err := s.BindPlot(ctx, BindPlotInput{
		PlotCode: "P1", StationCode: "GHOST", EffectiveDate: "2026-04-20",
	}); err == nil {
		t.Fatal("改绑到不存在的站应报错")
	}
}

// TestValidationErrors 各类非法输入。
func TestValidationErrors(t *testing.T) {
	s, _ := newTestService()
	ctx := context.Background()
	cases := []error{
		ValidateTemp(1, 2),
		ValidateTemp(200, 10),
		ValidateVariety(&model.Variety{Code: "x", BaseTemp: 30, UpperTemp: 30,
			Thresholds: []float64{1, 2, 3, 4, 5}}),
		ValidateVariety(&model.Variety{Code: "x", BaseTemp: 10, UpperTemp: 30,
			Thresholds: []float64{1, 3, 2, 4, 5}}),
		ValidateVariety(&model.Variety{Code: "x", BaseTemp: 10, UpperTemp: 30,
			Thresholds: []float64{1, 2, 3}}),
		ValidateSowQuery(time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)),
	}
	for i, err := range cases {
		if err == nil {
			t.Fatalf("第 %d 组非法输入应报错", i)
		}
	}
	// 合法对照。
	must(t, ValidateVariety(&model.Variety{Code: "x", BaseTemp: 10, UpperTemp: 30,
		Thresholds: []float64{1, 2, 3, 4, 5}}))
	must(t, ValidateSowQuery(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), testNow))
	// 播种晚于查询日（通过接口路径）。
	seedBase(t, s)
	if _, err := s.PlotStages(ctx, "NOPE", ""); err == nil {
		t.Fatal("不存在的地块查询应报错")
	}
}

// TestReachedAndForecast 已达到给实际日期；未达到用气候平均给预计。
func TestReachedAndForecast(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	// 给 S1 全年气候平均：恒温 25/15 -> GDD 10/天。
	for doy := 1; doy <= 366; doy++ {
		must(t, s.PutClimateNormal(ctx, model.ClimateNormal{
			StationCode: "S1", DOY: doy, TMax: 25, TMin: 15,
		}))
	}
	// 观测到 5 月 1 日：61 天 * 10 = 610。
	batch := make([]ObservationInput, 0)
	for i := 0; i < 61; i++ {
		day := sow.AddDate(0, 0, i)
		batch = append(batch, obs("S1", dstr(day), 25, 15, 1))
	}
	_, err := s.IngestObservations(ctx, batch)
	must(t, err)
	st := stagesOf(t, s, "P1")
	byStage := map[model.Stage]model.StageDate{}
	for _, x := range st {
		byStage[x.Stage] = x
	}
	// 出苗 30 在第 3 天（3/1、3/2 累计 20，3/3 累计 30），reached。
	e := byStage[model.StageEmergence]
	if e.Status != model.StatusReached || e.Date == nil || e.Date.Day() != 3 {
		t.Fatalf("出苗应为 reached 且为 3/3：%+v", e)
	}
	// 抽雄 400 在第 40 天（4/9），reached。
	ts := byStage[model.StageTasseling]
	if ts.Status != model.StatusReached || ts.Date == nil {
		t.Fatalf("抽雄应 reached：%+v", ts)
	}
	// 成熟 800 在第 80 天（offset79）= 5/19，在 asOf(6/1) 之前，
	// 前 61 天观测、其后气候平均，均为 10/天。
	mat := byStage[model.StageMaturity]
	if mat.Date == nil {
		t.Fatalf("成熟应能外推到日期")
	}
	if mat.Date.Day() != 19 || mat.Date.Month() != time.May {
		t.Fatalf("成熟应为 5/19，got %s", mat.Date.Format("2006-01-02"))
	}
}

// TestEventsDedup 阶段日期变化产生事件；同一变化不重复；事件可按游标拉取。
func TestEventsDedup(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	day := time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	// 初始：GDD 10/天。
	mk := func(tmax, tmin float64, seq int) {
		_, err := s.IngestObservations(ctx, []ObservationInput{obs("S1", dstr(day), tmax, tmin, seq)})
		must(t, err)
	}
	baseline := make([]ObservationInput, 0)
	for i := 0; i < 20; i++ {
		baseline = append(baseline, obs("S1", dstr(sow.AddDate(0, 0, i)), 20, 20, 1))
	}
	_, _ = s.IngestObservations(ctx, baseline)
	mk(20, 20, 1)
	n0 := len(eventsAll(t, s))
	// 同一条重复发：不再产生事件。
	mk(20, 20, 1)
	if len(eventsAll(t, s)) != n0 {
		t.Fatal("重复上报不应产生事件")
	}
	// 更正改变阶段日期：产生事件。
	mk(30, 30, 2)
	evs := eventsAll(t, s)
	if len(evs) <= n0 {
		t.Fatal("更正后应产生变更事件")
	}
	last := evs[len(evs)-1]
	if last.NewDate == nil || last.Reason == "" {
		t.Fatalf("事件应含新日期与原因：%+v", last)
	}
	if last.PlotCode != "P1" {
		t.Fatalf("事件地块错误：%s", last.PlotCode)
	}
	// 游标拉取。
	page, err := s.ListEvents(ctx, 0, 1, "")
	must(t, err)
	if len(page) != 1 {
		t.Fatalf("limit=1 应只返回 1 条，got %d", len(page))
	}
	page2, err := s.ListEvents(ctx, page[0].ID, 100, "")
	must(t, err)
	for _, e := range page2 {
		if e.ID <= page[0].ID {
			t.Fatal("游标之后的事件 ID 应严格更大")
		}
	}
	_ = fmt.Sprint
}

func eventsAll(t *testing.T, s *testSvc) []model.StageEvent {
	t.Helper()
	out, err := s.ListEvents(context.Background(), 0, 500, "")
	must(t, err)
	return out
}

// TestConcurrentSameStationDay 同站同日两条上报并发到达，最终保留序号大的，
// 且对应地块结果只按它计算（事件不重复）。
func TestConcurrentSameStationDay(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	day := "2026-04-07"
	// 背景数据，保证地块有结果。
	background := make([]ObservationInput, 0, 10)
	for i := 0; i < 10; i++ {
		background = append(background, obs("S1", dstr(sow.AddDate(0, 0, i)), 20, 20, 1))
	}
	_, err := s.IngestObservations(ctx, background)
	must(t, err)
	eventsBefore := len(eventsAll(t, s))

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		seq := 2
		tmax, tmin := 30.0, 10.0
		if i == 0 {
			seq = 3
			tmax, tmin = 26, 12 // 胜出：mean GDD 9
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.IngestObservations(ctx, []ObservationInput{obs("S1", day, tmax, tmin, seq)})
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		must(t, e)
	}
	rows := dailyOf(t, s, "P1")
	dd, _ := time.Parse("2006-01-02", day)
	for _, r := range rows {
		if r.Date.Equal(dd) && math.Abs(r.GDD-9) > 1e-9 {
			t.Fatalf("并发后应只按序号 3（GDD 9）计算，got %g", r.GDD)
		}
	}
	// 小序号那条可能先赢后被大序号更正：最终事件数有限，且幂等键保证无重复。
	evs := eventsAll(t, s)
	seen := map[string]bool{}
	for _, e := range evs[eventsBefore:] {
		key := string(e.Stage) + "|" + e.ChangeID
		if seen[key] {
			t.Fatalf("同一阶段同一变化出现重复事件：%s", key)
		}
		seen[key] = true
	}
}

// TestRestartResume 模拟“处理到一半重启”：直接删掉某地块部分快照，
// Recover 后结果与全量从头重算逐日一致。
func TestRestartResume(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "sine", sow)
	batch := make([]ObservationInput, 0, 50)
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 50; i++ {
		day := sow.AddDate(0, 0, i)
		tmin := 5 + rng.Float64()*12
		tmax := tmin + 6 + rng.Float64()*14
		batch = append(batch, obs("S1", dstr(day), tmax, tmin, 1))
	}
	// 邻站也有些数据，制造补值日。
	for i := 5; i < 50; i += 7 {
		day := sow.AddDate(0, 0, i)
		batch = append(batch, obs("S2", dstr(day), 28, 16, 1))
	}
	_, err := s.IngestObservations(ctx, batch)
	must(t, err)

	// 破坏快照：模拟中断留下的残缺状态（删掉自第 25 天起的行与阶段行）。
	cut := sow.AddDate(0, 0, 25)
	m.TruncateDailyFrom("P1", cut)

	// “重启”：新建 service 指向同一份数据，执行 Recover。
	s2 := NewService(m)
	s2.SetClock(func() time.Time { return testNow })
	must(t, s2.Recover(ctx))

	want := m.ReferenceDaily("P1", testNowDate())
	got := dailyOf(t, s2, "P1")
	assertDailyEqual(t, want, got)
}

// TestRolloverAdvancesAsOf 时钟跨天后 Rollover 把预测基准推进到新日期，
// 同一天内重复调用不产生额外写入。
func TestRolloverAdvancesAsOf(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	batch := make([]ObservationInput, 0, 40)
	for i := 0; i < 40; i++ {
		batch = append(batch, obs("S1", dstr(sow.AddDate(0, 0, i)), 25, 15, 1))
	}
	_, err := s.IngestObservations(ctx, batch)
	must(t, err)

	// 同一天内滚动：无操作。
	must(t, s.Rollover(ctx))
	eventsBefore := len(eventsAll(t, s))
	must(t, s.Rollover(ctx))
	if len(eventsAll(t, s)) != eventsBefore {
		t.Fatal("asOf 未变化时 Rollover 不应产生事件")
	}

	// 时钟前进 20 天：未来外推日的窗口应推进。
	newNow := testNow.AddDate(0, 0, 20)
	s.SetClock(func() time.Time { return newNow })
	must(t, s.Rollover(ctx))
	asOf := time.Date(newNow.Year(), newNow.Month(), newNow.Day(), 0, 0, 0, 0, time.UTC)
	want := m.ReferenceDaily("P1", asOf)
	assertDailyEqual(t, want, dailyOf(t, s, "P1"))

	// 再滚一次：无操作。
	must(t, s.Rollover(ctx))
}
