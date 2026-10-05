package engine

import (
	"context"
	"testing"
	"time"

	"agristation/internal/enginemem"
	"agristation/internal/model"
)

// crashTx 包装内存事务：在 ReplaceHistoricalStationYear 里先写入“半年”
// 内容再返回错误，模拟导入事务提交前进程被杀（delete 后、insert 未完成）。
type crashTx struct {
	enginemem.Tx
	armed bool
}

func (t crashTx) ReplaceHistoricalStationYear(station string, year int,
	rows []model.HistoricalWeather) error {
	write := rows
	if t.armed {
		// 只写入前一半，制造“半年数据”，随后让整个事务失败回滚。
		write = rows[:len(rows)/2]
	}
	if err := t.Tx.ReplaceHistoricalStationYear(station, year, write); err != nil {
		return err
	}
	if t.armed {
		return errSimulatedCrash
	}
	return nil
}

type errCrash string

func (e errCrash) Error() string { return string(e) }

const errSimulatedCrash errCrash = "模拟导入中途崩溃"

// crashStore 在 Update 里把内存事务包成 crashTx；View 用普通事务。
type crashStore struct {
	inner *enginemem.Store
	armed bool
}

func (c *crashStore) Update(ctx context.Context, fn func(crashTx) error) error {
	return c.inner.Update(ctx, func(tx enginemem.Tx) error {
		return fn(crashTx{Tx: tx, armed: c.armed})
	})
}

func (c *crashStore) View(ctx context.Context, fn func(crashTx) error) error {
	return c.inner.View(ctx, func(tx enginemem.Tx) error {
		return fn(crashTx{Tx: tx})
	})
}

// TestHistoricalCrashMidImportNoHalfYear 导入事务在“删了旧年、只写了半年”
// 时失败：整笔回滚，任何查询都只能看到旧的一整年；随后重启（新服务指向
// 同一数据）结果与没中断时相同。
func TestHistoricalCrashMidImportNoHalfYear(t *testing.T) {
	ctx := context.Background()
	mem := enginemem.New()
	cs := &crashStore{inner: mem, armed: true}
	svc := NewService[crashTx](cs)
	svc.SetClock(func() time.Time { return testNow })
	// 直接注册（辅助函数绑定了具体的 testSvc 类型）。
	if err := svc.RegisterStation(ctx, model.Station{Code: "S1", Name: "一",
		Latitude: 30, Longitude: 100, Elevation: 500}); err != nil {
		t.Fatal(err)
	}
	year := 2005

	// 先正常导入一整年（关闭崩溃开关）。
	cs.armed = false
	ingestFullYear := func(tmax, tmin float64) {
		t.Helper()
		in := make([]HistoricalInput, 0, 365)
		for d := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == year; d = d.AddDate(0, 0, 1) {
			in = append(in, histOne("S1", d.Format("2006-01-02"), tmax, tmin))
		}
		res, err := svc.IngestHistorical(ctx, in)
		if err != nil {
			t.Fatalf("正常导入失败：%v", err)
		}
		for _, it := range res.Items {
			if !it.Applied {
				t.Fatalf("正常导入应全部 applied：%+v", it)
			}
		}
	}
	ingestFullYear(30, 20)
	if rows := mem.HistoricalRows("S1", year); len(rows) != 365 {
		t.Fatalf("初始应 365 条，got %d", len(rows))
	}

	// 武装崩溃：整年替换走到一半事务失败。
	cs.armed = true
	in := make([]HistoricalInput, 0, 365)
	for d := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == year; d = d.AddDate(0, 0, 1) {
		in = append(in, histOne("S1", d.Format("2006-01-02"), 20, 10))
	}
	if _, err := svc.IngestHistorical(ctx, in); err == nil {
		t.Fatal("崩溃注入应使导入返回错误")
	}

	// 回滚后必须仍是旧的一整年 365 条、旧温度，绝看不到半年。
	rows := mem.HistoricalRows("S1", year)
	if len(rows) != 365 {
		t.Fatalf("崩溃后应仍是完整旧年 365 条，看到 %d 条（半年数据泄露）", len(rows))
	}
	for _, r := range rows {
		if r.TMax != 30 {
			t.Fatalf("崩溃后旧年内容被污染：tmax=%g", r.TMax)
		}
	}

	// “重启”：新建一个普通服务指向同一份数据，重新做同一次整年替换，
	// 应完整成功，结果与从未中断一致。
	svc2 := NewService(mem)
	svc2.SetClock(func() time.Time { return testNow })
	in2 := make([]HistoricalInput, 0, 365)
	for d := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == year; d = d.AddDate(0, 0, 1) {
		in2 = append(in2, histOne("S1", d.Format("2006-01-02"), 20, 10))
	}
	res2, err := svc2.IngestHistorical(ctx, in2)
	if err != nil {
		t.Fatalf("重启后重导失败：%v", err)
	}
	for _, it := range res2.Items {
		if !it.Applied {
			t.Fatalf("重启后重导应 applied：%+v", it)
		}
	}
	rows = mem.HistoricalRows("S1", year)
	if len(rows) != 365 {
		t.Fatalf("重启后重导应完整 365 条，got %d", len(rows))
	}
	for _, r := range rows {
		if r.TMax != 20 {
			t.Fatalf("重启后应为新年内容 tmax=20，got %g", r.TMax)
		}
	}
}
