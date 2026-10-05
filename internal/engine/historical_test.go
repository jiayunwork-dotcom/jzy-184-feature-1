package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"agristation/internal/model"
)

// histOne 构造一条历年记录输入。
func histOne(station, date string, tmax, tmin float64) HistoricalInput {
	return HistoricalInput{StationCode: station, Date: date, TMax: tmax, TMin: tmin}
}

// seedHistoricalYear 给某站导入一整年（1/1..12/31），温度由 fn 决定。
func seedHistoricalYear(t *testing.T, s *testSvc, station string, year int,
	fn func(d time.Time) (float64, float64)) {
	t.Helper()
	in := make([]HistoricalInput, 0, 366)
	for d := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == year; d = d.AddDate(0, 0, 1) {
		tmax, tmin := fn(d)
		in = append(in, histOne(station, d.Format("2006-01-02"), tmax, tmin))
	}
	res, err := s.IngestHistorical(context.Background(), in)
	if err != nil {
		t.Fatalf("导入 %s %d 失败：%v", station, year, err)
	}
	for _, it := range res.Items {
		if !it.OK || !it.Applied {
			t.Fatalf("站 %s %d 年应整年生效，第 %d 条 ok=%v applied=%v reason=%s",
				station, year, it.Index, it.OK, it.Applied, it.Reason)
		}
	}
}

// seedNormalsAll 给站写入全部 DOY 的气候平均，温度由 fn(DOY) 决定。
func seedNormalsAll(t *testing.T, s *testSvc, station string,
	fn func(doy int) (float64, float64)) {
	t.Helper()
	ctx := context.Background()
	for doy := 1; doy <= 366; doy++ {
		tmax, tmin := fn(doy)
		must(t, s.PutClimateNormal(ctx, model.ClimateNormal{
			StationCode: station, DOY: doy, TMax: tmax, TMin: tmin,
		}))
	}
}

// TestHistoricalIngestValidation 逐条非法原因 + 脏站年整体不生效 +
// 其余站年照常进入。
func TestHistoricalIngestValidation(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)

	res, err := s.IngestHistorical(ctx, []HistoricalInput{
		histOne("S1", "2005-06-01", 25, 15),                                     // 合法：A 组
		histOne("S1", "2005-06-02", 10, 20),                                     // 最低>最高：A 组变脏
		histOne("S1", "2005-06-03", 200, 10),                                    // 超范围：同组
		histOne("NOPE", "2005-06-01", 25, 15),                                   // 站不存在：B 组
		histOne("S2", "2005-06-01", 25, 15),                                     // 合法：C 组
		histOne("S2", "2005-06-02", 26, 16),                                     // 合法：C 组
		{StationCode: "S2", Year: 2006, Date: "2005-06-03", TMax: 25, TMin: 15}, // 日期不属于年份
		{StationCode: "S2", Date: "bad-date", TMax: 25, TMin: 15},               // 日期格式
	})
	must(t, err)

	byIdx := map[int]HistoricalItemResult{}
	for _, it := range res.Items {
		byIdx[it.Index] = it
	}
	// A 组（0..2）：站年整体不生效。第 0 条本身合法（ok=true）但
	// applied=false（被同组非法记录连累）；第 1、2 条本身非法 ok=false。
	if byIdx[0].OK != true || byIdx[0].Applied {
		t.Fatalf("A 组合法条应 ok=true/applied=false：%+v", byIdx[0])
	}
	if !contains(byIdx[0].Reason, "整体未导入") {
		t.Fatalf("合法条应带连累原因：%q", byIdx[0].Reason)
	}
	if byIdx[1].OK || byIdx[1].Applied {
		t.Fatalf("A 组非法条不应 ok/applied：%+v", byIdx[1])
	}
	if byIdx[2].OK {
		t.Fatalf("A 组非法条不应 ok：%+v", byIdx[2])
	}
	for _, i := range []int{0, 1, 2} {
		if byIdx[i].Reason == "" {
			t.Fatalf("第 %d 条应带原因", i)
		}
	}
	// 具体原因区分。
	if byIdx[1].Reason == "" || !contains(byIdx[1].Reason, "最低气温") {
		t.Fatalf("最低>最高原因不对：%q", byIdx[1].Reason)
	}
	if !contains(byIdx[3].Reason, "不存在") {
		t.Fatalf("站不存在原因不对：%q", byIdx[3].Reason)
	}
	if !contains(byIdx[6].Reason, "不属于所标的年份") {
		t.Fatalf("年份不符原因不对：%q", byIdx[6].Reason)
	}
	if !contains(byIdx[7].Reason, "YYYY-MM-DD") {
		t.Fatalf("日期格式原因不对：%q", byIdx[7].Reason)
	}
	// C 组（4、5）干净，照常生效。
	for _, i := range []int{4, 5} {
		if !byIdx[i].OK || !byIdx[i].Applied {
			t.Fatalf("干净站年第 %d 条应生效：%+v", i, byIdx[i])
		}
	}
	// 年级别汇总：3 个站年，仅 S2 2005 替换成功。
	var replaced int
	for _, y := range res.Years {
		if y.Replaced {
			replaced++
			if y.StationCode != "S2" || y.Year != 2005 || y.Records != 2 {
				t.Fatalf("替换的站年不对：%+v", y)
			}
		}
	}
	if replaced != 1 {
		t.Fatalf("应有 1 个站年替换，got %d：%+v", replaced, res.Years)
	}
}

// TestHistoricalDuplicateInBatch 同批同站同日重复：两条都报原因，整年不生效。
func TestHistoricalDuplicateInBatch(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	res, err := s.IngestHistorical(ctx, []HistoricalInput{
		histOne("S1", "2005-06-01", 25, 15),
		histOne("S1", "2005-06-01", 26, 16),
	})
	must(t, err)
	for _, it := range res.Items {
		if it.OK || it.Applied {
			t.Fatalf("重复日两条都不应生效：%+v", it)
		}
		if !contains(it.Reason, "重复") {
			t.Fatalf("应报重复原因：%q", it.Reason)
		}
	}
}

// TestHistoricalWholeYearReplace 同站同年再导一次：整年换成新内容；
// 若新内容有非法记录导致整年不生效，旧的一整年原样保留（崩溃/回滚语义）。
func TestHistoricalWholeYearReplace(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService()
	seedBase(t, s)
	year := 2005

	constWarm := func(time.Time) (float64, float64) { return 30, 20 }
	constCold := func(time.Time) (float64, float64) { return 20, 12 }
	seedHistoricalYear(t, s, "S1", year, constWarm)
	rows := m.HistoricalRows("S1", year)
	if len(rows) != 365 {
		t.Fatalf("应有 365 条，got %d", len(rows))
	}
	for _, r := range rows {
		if r.TMax != 30 {
			t.Fatalf("初次导入内容不对")
		}
	}

	// 整年替换成冷年。
	seedHistoricalYear(t, s, "S1", year, constCold)
	rows = m.HistoricalRows("S1", year)
	if len(rows) != 365 {
		t.Fatalf("替换后仍应 365 条")
	}
	for _, r := range rows {
		if r.TMax != 20 {
			t.Fatalf("替换未整体生效：tmax=%g", r.TMax)
		}
	}

	// 再导一次但夹一条非法：整年不生效，旧冷年原样保留。
	bad := []HistoricalInput{
		histOne("S1", "2005-01-01", 31, 21),
		histOne("S1", "2005-01-02", 5, 50), // 非法
	}
	res, err := s.IngestHistorical(ctx, bad)
	must(t, err)
	// 第一条本身合法但因同站同年有非法记录而整年不生效；第二条非法。
	if res.Items[0].Applied {
		t.Fatalf("脏导入合法条也不应 applied")
	}
	if res.Items[1].OK || res.Items[1].Applied {
		t.Fatalf("脏导入非法条不应 ok/applied")
	}
	rows = m.HistoricalRows("S1", year)
	if len(rows) != 365 {
		t.Fatalf("脏导入回滚后应仍是完整旧年 365 条，got %d", len(rows))
	}
	for _, r := range rows {
		if r.TMax != 20 {
			t.Fatalf("脏导入不得污染旧年：tmax=%g", r.TMax)
		}
	}
}

// TestHistoricalConcurrentSameStationYear 同站同年两个并发导入：
// 最终必须完整等于其中一次，不能出现混合内容。
func TestHistoricalConcurrentSameStationYear(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService()
	seedBase(t, s)
	year := 2005

	mk := func(tmax, tmin float64) []HistoricalInput {
		in := make([]HistoricalInput, 0, 365)
		for d := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == year; d = d.AddDate(0, 0, 1) {
			in = append(in, histOne("S1", d.Format("2006-01-02"), tmax, tmin))
		}
		return in
	}
	batchA := mk(30, 20)
	batchB := mk(22, 10)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		b := batchA
		if i == 1 {
			b = batchB
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.IngestHistorical(ctx, b)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		must(t, e)
	}

	var rows []model.HistoricalWeather
	rows = m.HistoricalRows("S1", year)
	if len(rows) != 365 {
		t.Fatalf("应保留一整年 365 条，got %d", len(rows))
	}
	tmax := rows[0].TMax
	if tmax != 30 && tmax != 22 {
		t.Fatalf("内容来源无法识别：tmax=%g", tmax)
	}
	for _, r := range rows {
		if r.TMax != tmax {
			t.Fatalf("并发导入后出现混合内容：首日 %g，遇到 %g", tmax, r.TMax)
		}
	}
}

// TestHistoricalNeverTouchesExisting 导入历年资料不得改变单点预测、逐日
// 快照与阶段事件（原有功能不受影响）。
func TestHistoricalNeverTouchesExisting(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	batch := make([]ObservationInput, 0, 40)
	for i := 0; i < 40; i++ {
		batch = append(batch, obs("S1", dstr(sow.AddDate(0, 0, i)), 25, 15, 1))
	}
	_, err := s.IngestObservations(ctx, batch)
	must(t, err)

	beforeDaily := dailyOf(t, s, "P1")
	beforeStages := stagesOf(t, s, "P1")
	evBefore := len(eventsAll(t, s))

	for y := 2000; y <= 2006; y++ {
		seedHistoricalYear(t, s, "S1", y, func(time.Time) (float64, float64) { return 40, 30 })
	}
	afterDaily := dailyOf(t, s, "P1")
	afterStages := stagesOf(t, s, "P1")
	evAfter := len(eventsAll(t, s))

	assertDailyEqual(t, beforeDaily, afterDaily)
	if len(beforeStages) != len(afterStages) {
		t.Fatalf("阶段行数变化")
	}
	for i := range beforeStages {
		a, b := beforeStages[i], afterStages[i]
		if a.Status != b.Status || !datesEqual(a.Date, b.Date) {
			t.Fatalf("导入历年后阶段 %s 结果被改变：%+v vs %+v", a.Stage, a, b)
		}
	}
	if evBefore != evAfter {
		t.Fatalf("导入历年不应产生阶段事件：%d -> %d", evBefore, evAfter)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
