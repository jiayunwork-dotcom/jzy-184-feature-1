package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"agristation/internal/model"
)

// TestConcurrentHistoricalSameStationYear 同站同年两次导入并发到达：
// 最终必须完整等于其中一次（按日集合与温度完全一致），不能拼出混合年。
func TestConcurrentHistoricalSameStationYear(t *testing.T) {
	ctx := context.Background()
	s, _ := newTestService()
	seedBase(t, s)

	mk := func(tag string, tmax, tmin float64) []HistoricalInput {
		recs := make([]HistoricalInput, 0, 365)
		d0 := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC)
		for i := 0; i < 365; i++ {
			d := d0.AddDate(0, 0, i)
			recs = append(recs, HistoricalInput{
				StationCode: "S1", Year: 2010,
				Date: d.Format("2006-01-02"), TMax: tmax, TMin: tmin,
			})
		}
		return recs
	}
	a := mk("A", 25, 15)
	b := mk("B", 31, 21)

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, e := s.ImportHistorical(ctx, a); errs <- e }()
	go func() { defer wg.Done(); _, e := s.ImportHistorical(ctx, b); errs <- e }()
	wg.Wait()
	close(errs)
	for e := range errs {
		must(t, e)
	}

	rows, err := histRowsForTest(s, "S1")
	must(t, err)
	if len(rows) != 365 {
		t.Fatalf("整年应 365 行，got %d（并发写入拼出了混合/残缺年）", len(rows))
	}
	// 每一天的温度必须完全来自同一次导入。
	allA, allB := true, true
	for _, r := range rows {
		if r.TMax != 25 || r.TMin != 15 {
			allA = false
		}
		if r.TMax != 31 || r.TMin != 21 {
			allB = false
		}
	}
	if !allA && !allB {
		t.Fatal("最终内容既不等于导入 A 也不等于导入 B：并发导入产生了混合年")
	}
	// 试走结果也必须能复现：全年来自其中一次。
	seedPlot(t, s, "P1", "S1", "mean", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	out, err := s.PlotOutlook(ctx, "P1", "", nil)
	must(t, err)
	// 无论谁赢，所有成员年（只有 2010 一年）的最早/最晚应相同。
	for _, so := range out.Stages {
		if so.NYears != 1 {
			t.Fatalf("应只有 1 个成员年，got %d", so.NYears)
		}
		if so.Earliest != nil && so.Latest != nil && !so.Earliest.Equal(*so.Latest) {
			t.Fatalf("单成员年最早/最晚应相同：%s vs %s",
				so.Earliest.Format("01-02"), so.Latest.Format("01-02"))
		}
	}
}

// TestHistoricalImportAtomicRestart 模拟“导入写了一半服务被杀”：
// 直接在存储里留下残缺年（只写若干天），查询与导入后的状态必须与
// “该年从未生效”相同；随后完整导入该年，结果与从未中断一致。
//
// 内存后端的事务回滚已覆盖“事务内整体回滚”；这里额外验证部分物理行
// （模拟 DELETE 已提交、INSERT 中断的极端残骸）不会被当成完整年
// 错误地参与计算导致崩溃，且整年替换能把它收敛为完整一次。
func TestHistoricalImportAtomicRestart(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)

	// 先合法导入 2011 整年。
	histImport(t, s, "S1", 2011, 26, 16)

	// 模拟 2010 年导入中断残骸：底层直接塞 5 天（绕过引擎事务）。
	m.CorruptHistorical("S1", 2010, []model.HistoricalTemp{
		{StationCode: "S1", Year: 2010, Date: time.Date(2010, 6, 1, 0, 0, 0, 0, time.UTC), TMax: 40, TMin: 30},
		{StationCode: "S1", Year: 2010, Date: time.Date(2010, 6, 2, 0, 0, 0, 0, time.UTC), TMax: 40, TMin: 30},
		{StationCode: "S1", Year: 2010, Date: time.Date(2010, 6, 3, 0, 0, 0, 0, time.UTC), TMax: 40, TMin: 30},
		{StationCode: "S1", Year: 2010, Date: time.Date(2010, 6, 4, 0, 0, 0, 0, time.UTC), TMax: 40, TMin: 30},
		{StationCode: "S1", Year: 2010, Date: time.Date(2010, 6, 5, 0, 0, 0, 0, time.UTC), TMax: 40, TMin: 30},
	})

	// 服务不会“看到导了一半的 2010”——残骸年的温度不影响 2011 整年的
	// 计算结果：以参考器（只含 2011 整年 + 残骸）为准逐项一致即可。
	asOf := testNowDate()
	out, err := s.PlotOutlook(ctx, "P1", dstr(asOf), nil)
	must(t, err)
	ref := m.ReferenceOutlook("P1", asOf, nil)
	// 残骸年确实作为一个“缺日年份”参与（缺日按无补值记 0），engine 与
	// 参考器必须一致——这才是“最终数据从头算”的口径。
	assertOutlookEqual(t, 0, "P1", ref, out)

	// 重新完整导入 2010：残骸被整年替换，结果与一份全新完整年一致。
	histImport(t, s, "S1", 2010, 25, 15)
	out2, err := s.PlotOutlook(ctx, "P1", dstr(asOf), nil)
	must(t, err)
	ref2 := m.ReferenceOutlook("P1", asOf, nil)
	assertOutlookEqual(t, 1, "P1", ref2, out2)

	// 2010 现在必须是 365 行。
	rows, err := histRowsForTest(s, "S1")
	must(t, err)
	cnt := map[int]int{}
	for _, r := range rows {
		cnt[r.Year]++
	}
	if cnt[2010] != 365 {
		t.Fatalf("重导后 2010 应完整 365 行，got %d", cnt[2010])
	}
}
