package engine

import (
	"context"
	"testing"
	"time"
)

// TestHistoricalImportCrashRollback 整年导入写到一半（DELETE 已执行、
// INSERT 进行中）时服务被杀：整站年随事务回滚，查询看不到导了一半的
// 一年；重启后完整重导，结果与从未中断一致。
func TestHistoricalImportCrashRollback(t *testing.T) {
	ctx := context.Background()
	s, m := newTestService()
	seedBase(t, s)
	sow := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	seedPlot(t, s, "P1", "S1", "mean", sow)
	histImport(t, s, "S1", 2011, 26, 16)

	before, err := histRowsForTest(s, "S1")
	must(t, err)

	// 打开“替换到一半崩溃”开关：DELETE 之后 panic，模拟进程被杀。
	m.SetFailHistoricalReplace(true)
	func() {
		defer func() { _ = recover() }()
		recs := mkYearFixed("S1", 2010, 25, 15)
		_, _ = s.ImportHistorical(ctx, recs)
	}()
	m.SetFailHistoricalReplace(false)

	// 崩溃后：2010 一年都看不到，行数与崩溃前完全相同。
	after, err := histRowsForTest(s, "S1")
	must(t, err)
	if len(after) != len(before) {
		t.Fatalf("崩溃回滚后行数应不变：before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		if !before[i].Date.Equal(after[i].Date) || before[i].TMax != after[i].TMax {
			t.Fatalf("崩溃后既有数据被改动：第 %d 行", i)
		}
	}

	// 试走结果与参考器（最终数据 = 只有 2011 整年）逐项一致。
	asOf := testNowDate()
	o1, err := s.PlotOutlook(ctx, "P1", dstr(asOf), nil)
	must(t, err)
	assertOutlookEqual(t, 0, "P1", m.ReferenceOutlook("P1", asOf, nil), o1)

	// 重启（新 service 指同一份数据）后完整重导：与从未中断一致。
	s2 := NewService(m)
	s2.SetClock(func() time.Time { return testNow })
	histImport(t, s2, "S1", 2010, 25, 15)
	o2, err := s2.PlotOutlook(ctx, "P1", dstr(asOf), nil)
	must(t, err)
	assertOutlookEqual(t, 1, "P1", m.ReferenceOutlook("P1", asOf, nil), o2)

	rows, err := histRowsForTest(s2, "S1")
	must(t, err)
	if len(rows) != 730 {
		t.Fatalf("重导后应为两年共 730 行，got %d", len(rows))
	}
}
