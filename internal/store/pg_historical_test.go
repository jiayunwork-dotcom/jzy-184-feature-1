package store

import (
	"context"
	"math"
	"testing"
	"time"

	"agristation/internal/engine"
	"agristation/internal/enginemem"
	"agristation/internal/model"
)

// histYearInputs 构造某站某年一整年的导入记录（恒温）。
func histYearInputs(station string, year int, tmax, tmin float64) []engine.HistoricalInput {
	in := make([]engine.HistoricalInput, 0, 365)
	for d := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == year; d = d.AddDate(0, 0, 1) {
		in = append(in, engine.HistoricalInput{
			StationCode: station,
			Date:        d.Format("2006-01-02"),
			TMax:        tmax, TMin: tmin,
		})
	}
	return in
}

// applyToBoth 把同一批历年记录分别导入 PG 与内存后端。
func applyHistToBoth(t *testing.T, pg, mem interface {
	IngestHistorical(context.Context, []engine.HistoricalInput) (*engine.IngestHistoricalResult, error)
}, in []engine.HistoricalInput) {
	t.Helper()
	r1, err := pg.IngestHistorical(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := mem.IngestHistorical(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Items) != len(r2.Items) {
		t.Fatalf("PG/内存逐条结果条数不一致：%d vs %d", len(r1.Items), len(r2.Items))
	}
	for i := range r1.Items {
		if r1.Items[i].OK != r2.Items[i].OK || r1.Items[i].Applied != r2.Items[i].Applied {
			t.Fatalf("第 %d 条 PG(%v,%v) vs 内存(%v,%v)",
				i, r1.Items[i].OK, r1.Items[i].Applied, r2.Items[i].OK, r2.Items[i].Applied)
		}
	}
}

// TestPGHistoricalRangeParity 历年导入 + 范围/分位/比例：PG 与内存两后端
// 在同样最终数据上逐项相等。
func TestPGHistoricalRangeParity(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := New(pool)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	pgSvc := engine.NewService[Tx](st)
	pgSvc.SetClock(func() time.Time { return asOf })

	mem := enginemem.New()
	memSvc := engine.NewService[enginemem.Tx](mem)
	memSvc.SetClock(func() time.Time { return asOf })

	station := model.Station{Code: "S1", Name: "一", Latitude: 30.0, Longitude: 100.0, Elevation: 500}
	if err := pgSvc.RegisterStation(ctx, station); err != nil {
		t.Fatal(err)
	}
	if err := memSvc.RegisterStation(ctx, station); err != nil {
		t.Fatal(err)
	}
	v := model.Variety{Code: "V1", BaseTemp: 10, UpperTemp: 30,
		Thresholds: []float64{100, 400, 900, 1400, 2000}}
	if err := pgSvc.RegisterVariety(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := memSvc.RegisterVariety(ctx, v); err != nil {
		t.Fatal(err)
	}
	p := model.Plot{Code: "P1", SowDate: asOf, Variety: "V1", Method: "mean"}
	if _, err := pgSvc.RegisterPlot(ctx, p, "S1"); err != nil {
		t.Fatal(err)
	}
	if _, err := memSvc.RegisterPlot(ctx, p, "S1"); err != nil {
		t.Fatal(err)
	}
	// 两端写同样的全年气候平均（随 DOY 变化）。
	for doy := 1; doy <= 366; doy++ {
		tmax := 21 + 4*math.Sin(float64(doy)*0.025)
		cn := model.ClimateNormal{StationCode: "S1", DOY: doy, TMax: tmax, TMin: tmax - 8}
		if err := pgSvc.PutClimateNormal(ctx, cn); err != nil {
			t.Fatal(err)
		}
		if err := memSvc.PutClimateNormal(ctx, cn); err != nil {
			t.Fatal(err)
		}
	}

	// 6 个冷暖不同的年（mean GDD 6..16）。
	for i := 0; i < 6; i++ {
		y := 2000 + i
		g := 6.0 + float64(i)*2
		applyHistToBoth(t, pgSvc, memSvc, histYearInputs("S1", y, 10+g+4, 10+g-4))
	}

	compare := func(stage string) {
		t.Helper()
		pr, err := pgSvc.StageRanges(ctx, "P1", []float64{0.1, 0.5, 0.9}, "")
		if err != nil {
			t.Fatal(err)
		}
		mr, err := memSvc.StageRanges(ctx, "P1", []float64{0.1, 0.5, 0.9}, "")
		if err != nil {
			t.Fatal(err)
		}
		if pr.Available != mr.Available || len(pr.Ranges) != len(mr.Ranges) {
			t.Fatalf("%s available/行数不一致", stage)
		}
		for i := range pr.Ranges {
			pg, mm := pr.Ranges[i], mr.Ranges[i]
			if pg.Stage != mm.Stage || pg.YearsUsed != mm.YearsUsed ||
				pg.NotReachedYears != mm.NotReachedYears || pg.Reached != mm.Reached {
				t.Fatalf("%s 阶段 %s 汇总不一致：PG=%+v MEM=%+v", stage, pg.Stage, pg, mm)
			}
			if (pg.Earliest == nil) != (mm.Earliest == nil) {
				t.Fatalf("%s 阶段 %s earliest 有无不一致", stage, pg.Stage)
			}
			if pg.Earliest != nil && !pg.Earliest.Equal(*mm.Earliest) {
				t.Fatalf("%s 阶段 %s earliest %s vs %s",
					stage, pg.Stage, pg.Earliest.Format("01-02"), mm.Earliest.Format("01-02"))
			}
			if (pg.Latest == nil) != (mm.Latest == nil) {
				t.Fatalf("%s 阶段 %s latest 有无不一致", stage, pg.Stage)
			}
			if pg.Latest != nil && !pg.Latest.Equal(*mm.Latest) {
				t.Fatalf("%s 阶段 %s latest %s vs %s",
					stage, pg.Stage, pg.Latest.Format("01-02"), mm.Latest.Format("01-02"))
			}
			for qi := range pg.Quantiles {
				pq, mq := pg.Quantiles[qi], mm.Quantiles[qi]
				if pq.Reachable != mq.Reachable {
					t.Fatalf("%s 阶段 %s 分位 %g reachable 不一致",
						stage, pg.Stage, pq.Quantile)
				}
				if (pq.Date == nil) != (mq.Date == nil) {
					t.Fatalf("%s 阶段 %s 分位 %g 日期有无不一致",
						stage, pg.Stage, pq.Quantile)
				}
				if pq.Date != nil && !pq.Date.Equal(*mq.Date) {
					t.Fatalf("%s 阶段 %s 分位 %g %s vs %s",
						stage, pg.Stage, pq.Quantile,
						pq.Date.Format("01-02"), mq.Date.Format("01-02"))
				}
			}
		}
		// 比例抽点。
		for _, off := range []int{0, 30, 90, 160, 269} {
			d := asOf.AddDate(0, 0, off).Format("2006-01-02")
			pa, err := pgSvc.ArrivalBy(ctx, "P1", model.StageMaturity, d, "")
			if err != nil {
				t.Fatal(err)
			}
			ma, err := memSvc.ArrivalBy(ctx, "P1", model.StageMaturity, d, "")
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(pa.Fraction-ma.Fraction) > 1e-12 {
				t.Fatalf("%s 成熟比例 off=%d PG=%g MEM=%g", stage, off, pa.Fraction, ma.Fraction)
			}
		}
	}
	compare("initial")

	// 再做一次整年替换（把 2002 换成冷年），重对账。
	applyHistToBoth(t, pgSvc, memSvc, histYearInputs("S1", 2002, 16, 8))
	compare("after-replace")

	// 脏导入（夹非法）：两后端都必须整年不生效，范围不变。
	before, _ := pgSvc.StageRanges(ctx, "P1", nil, "")
	bad := []engine.HistoricalInput{
		{StationCode: "S1", Date: "2003-01-01", TMax: 25, TMin: 15},
		{StationCode: "S1", Date: "2003-01-02", TMax: 10, TMin: 20},
	}
	r, err := pgSvc.IngestHistorical(ctx, bad)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range r.Items {
		if it.Applied {
			t.Fatalf("脏导入不应 applied：%+v", it)
		}
	}
	after, _ := pgSvc.StageRanges(ctx, "P1", nil, "")
	for i := range before.Ranges {
		b, a := before.Ranges[i], after.Ranges[i]
		if b.YearsUsed != a.YearsUsed || b.NotReachedYears != a.NotReachedYears {
			t.Fatalf("脏导入回滚后范围变化：阶段 %s", b.Stage)
		}
	}
	compare("after-dirty-rollback")
}
