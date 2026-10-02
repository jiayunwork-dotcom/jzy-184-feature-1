package store

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"agristation/internal/engine"
	"agristation/internal/enginemem"
	"agristation/internal/model"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 该测试需要真实 PostgreSQL；未设置 DATABASE_URL 时跳过。
// 本地：DATABASE_URL=postgres://agri@localhost:5439/agristation?sslmode=disable
// Docker Compose：在 app 容器内指向 db 服务。
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("未设置 DATABASE_URL，跳过 PostgreSQL 集成测试")
	}

	// 先用临时连接建独立 schema，避免重复运行相互干扰。
	admin, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("连接 PG 失败：%v", err)
	}
	if err := admin.Ping(context.Background()); err != nil {
		t.Fatalf("Ping PG 失败：%v", err)
	}
	schema := fmt.Sprintf("it_%d", time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("建 schema 失败：%v", err)
	}
	admin.Close()

	// 正式连接池：每个新连接都设置 search_path 到该 schema。
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsnSchema := dsn + sep + "options=-c%20search_path%3D" + schema
	pool, err := pgxpool.New(context.Background(), dsnSchema)
	if err != nil {
		t.Fatalf("连接 PG 失败：%v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		// 用临时连接清理。
		if c, err := pgxpool.New(context.Background(), dsn); err == nil {
			_, _ = c.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
			c.Close()
		}
	})
	return pool
}

// TestPGParityAndRecovery 是 PostgreSQL 版核心一致性测试：
//  1. 同一随机操作序列分别打到 PG 后端和内存后端，逐日/阶段结果必须一致；
//  2. 中途和结尾调用 Recover（用最终数据从头全量重建），结果不变，
//     且不产生新事件（增量结果 == 全量重算）。
func TestPGParityAndRecovery(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	pgStore := New(pool)
	if err := pgStore.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	fixedNow := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pgSvc := engine.NewService[Tx](pgStore)
	pgSvc.SetClock(func() time.Time { return fixedNow })

	mem := enginemem.New()
	memSvc := engine.NewService[enginemem.Tx](mem)
	memSvc.SetClock(func() time.Time { return fixedNow })

	stations := []model.Station{
		{Code: "S1", Name: "一", Latitude: 30.0, Longitude: 100.0, Elevation: 500},
		{Code: "S2", Name: "二", Latitude: 30.01, Longitude: 100.01, Elevation: 520},
		{Code: "S3", Name: "三", Latitude: 30.5, Longitude: 100.4, Elevation: 600},
	}
	for _, st := range stations {
		if err := pgSvc.RegisterStation(ctx, st); err != nil {
			t.Fatal(err)
		}
		if err := memSvc.RegisterStation(ctx, st); err != nil {
			t.Fatal(err)
		}
	}
	v := model.Variety{
		Code: "V1", Name: "品种", BaseTemp: 10, UpperTemp: 30,
		Thresholds: []float64{30, 200, 400, 460, 800},
	}
	if err := pgSvc.RegisterVariety(ctx, v); err != nil {
		t.Fatal(err)
	}
	if err := memSvc.RegisterVariety(ctx, v); err != nil {
		t.Fatal(err)
	}
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if _, err := pgSvc.RegisterPlot(ctx, model.Plot{
		Code: "P1", SowDate: sow, Variety: "V1", Method: "sine",
	}, "S1"); err != nil {
		t.Fatal(err)
	}
	if _, err := memSvc.RegisterPlot(ctx, model.Plot{
		Code: "P1", SowDate: sow, Variety: "V1", Method: "sine",
	}, "S1"); err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(20260930))
	maxSeq := map[string]int{}
	temps := func() (float64, float64) {
		tmin := 2 + rng.Float64()*20
		return tmin + 3 + rng.Float64()*18, tmin
	}
	ds := func(off int) string { return sow.AddDate(0, 0, off).Format("2006-01-02") }

	compare := func(stage string) {
		t.Helper()
		pd, err := pgSvc.PlotDaily(ctx, "P1", "", "", "")
		if err != nil {
			t.Fatalf("%s: pg daily: %v", stage, err)
		}
		md, err := memSvc.PlotDaily(ctx, "P1", "", "", "")
		if err != nil {
			t.Fatalf("%s: mem daily: %v", stage, err)
		}
		if len(pd) != len(md) {
			t.Fatalf("%s: 行数 %d vs %d", stage, len(pd), len(md))
		}
		for i := range pd {
			if !pd[i].Date.Equal(md[i].Date) {
				t.Fatalf("%s: 行 %d 日期 %s vs %s", stage, i,
					pd[i].Date.Format("01-02"), md[i].Date.Format("01-02"))
			}
			if d := pd[i].GDD - md[i].GDD; d > 1e-9 || d < -1e-9 {
				t.Fatalf("%s: %s GDD pg=%g mem=%g", stage,
					pd[i].Date.Format("01-02"), pd[i].GDD, md[i].GDD)
			}
			if d := pd[i].Cumulative - md[i].Cumulative; d > 1e-9 || d < -1e-9 {
				t.Fatalf("%s: %s 累计 pg=%g mem=%g", stage,
					pd[i].Date.Format("01-02"), pd[i].Cumulative, md[i].Cumulative)
			}
			if pd[i].Source != md[i].Source {
				t.Fatalf("%s: %s 来源 pg=%s mem=%s", stage,
					pd[i].Date.Format("01-02"), pd[i].Source, md[i].Source)
			}
		}
		ps, err := pgSvc.PlotStages(ctx, "P1", "")
		if err != nil {
			t.Fatal(err)
		}
		ms, err := memSvc.PlotStages(ctx, "P1", "")
		if err != nil {
			t.Fatal(err)
		}
		for i := range ps {
			if (ps[i].Date == nil) != (ms[i].Date == nil) {
				t.Fatalf("%s: 阶段 %s 日期有无不一致", stage, ps[i].Stage)
			}
			if ps[i].Date != nil && !ps[i].Date.Equal(*ms[i].Date) {
				t.Fatalf("%s: 阶段 %s pg=%s mem=%s", stage, ps[i].Stage,
					ps[i].Date.Format("01-02"), ms[i].Date.Format("01-02"))
			}
			if ps[i].Status != ms[i].Status {
				t.Fatalf("%s: 阶段 %s 状态 pg=%s mem=%s", stage, ps[i].Stage, ps[i].Status, ms[i].Status)
			}
		}
	}

	const steps = 200
	for step := 0; step < steps; step++ {
		off := rng.Intn(70)
		day := ds(off)
		switch rng.Intn(8) {
		case 0, 1, 2, 3:
			key := "S1|" + day
			seq := maxSeq[key] + 1
			maxSeq[key] = seq
			tmax, tmin := temps()
			in := []engine.ObservationInput{{StationCode: "S1", Date: day, TMax: tmax, TMin: tmin, Seq: seq}}
			if _, err := pgSvc.IngestObservations(ctx, in); err != nil {
				t.Fatal(err)
			}
			if _, err := memSvc.IngestObservations(ctx, in); err != nil {
				t.Fatal(err)
			}
		case 4:
			// 乱序小序号。
			in := []engine.ObservationInput{{StationCode: "S1", Date: day, TMax: 40, TMin: 30, Seq: 1}}
			_, _ = pgSvc.IngestObservations(ctx, in)
			_, _ = memSvc.IngestObservations(ctx, in)
		case 5:
			// 邻站观测，影响补值。
			tmax, tmin := temps()
			in := []engine.ObservationInput{{StationCode: "S2", Date: day, TMax: tmax, TMin: tmin, Seq: maxSeq["S2|"+day] + 1}}
			maxSeq["S2|"+day]++
			if _, err := pgSvc.IngestObservations(ctx, in); err != nil {
				t.Fatal(err)
			}
			if _, err := memSvc.IngestObservations(ctx, in); err != nil {
				t.Fatal(err)
			}
		case 6:
			// 批量。
			batch := make([]engine.ObservationInput, 0, 4)
			for k := 0; k < 4; k++ {
				o2 := rng.Intn(70)
				day2 := ds(o2)
				tmax, tmin := temps()
				batch = append(batch, engine.ObservationInput{
					StationCode: "S1", Date: day2, TMax: tmax, TMin: tmin, Seq: maxSeq["S1|"+day2] + 1,
				})
				maxSeq["S1|"+day2]++
			}
			if _, err := pgSvc.IngestObservations(ctx, batch); err != nil {
				t.Fatal(err)
			}
			if _, err := memSvc.IngestObservations(ctx, batch); err != nil {
				t.Fatal(err)
			}
		case 7:
			// 改绑到 S2。
			in := engine.BindPlotInput{PlotCode: "P1", StationCode: "S2", EffectiveDate: day}
			if err := pgSvc.BindPlot(ctx, in); err != nil {
				t.Fatal(err)
			}
			if err := memSvc.BindPlot(ctx, in); err != nil {
				t.Fatal(err)
			}
		}
		compare(fmt.Sprintf("step %d", step))

		// 每 40 步做一次“重启续跑”：Recover 全量重建后必须与当前结果一致。
		if step%40 == 39 {
			evBefore, err := pgSvc.ListEvents(ctx, 0, 5000, "")
			if err != nil {
				t.Fatal(err)
			}
			before, _ := pgSvc.PlotDaily(ctx, "P1", "", "", "")
			if err := pgSvc.Recover(ctx); err != nil {
				t.Fatalf("Recover: %v", err)
			}
			after, _ := pgSvc.PlotDaily(ctx, "P1", "", "", "")
			if len(before) != len(after) {
				t.Fatalf("step %d Recover 后行数变化：%d vs %d", step, len(before), len(after))
			}
			for i := range before {
				if d := before[i].GDD - after[i].GDD; d > 1e-9 || d < -1e-9 {
					t.Fatalf("step %d Recover 后 %s GDD 漂移 %g", step,
						before[i].Date.Format("01-02"), d)
				}
			}
			evAfter, err := pgSvc.ListEvents(ctx, 0, 5000, "")
			if err != nil {
				t.Fatal(err)
			}
			if len(evAfter) != len(evBefore) {
				t.Fatalf("数据未变，Recover 不应产生新事件：%d -> %d",
					len(evBefore), len(evAfter))
			}
			compare(fmt.Sprintf("step %d post-recover", step))
		}
	}
}

// TestPGAdvisoryLockConcurrent 同站同日并发上报：最终保留大序号。
func TestPGAdvisoryLockConcurrent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := New(pool)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	svc := engine.NewService[Tx](st)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(svc.RegisterStation(ctx, model.Station{Code: "S1", Latitude: 30, Longitude: 100}))
	must(svc.RegisterVariety(ctx, model.Variety{
		Code: "V1", BaseTemp: 10, UpperTemp: 30,
		Thresholds: []float64{30, 200, 400, 460, 800},
	}))
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	_, err := svc.RegisterPlot(ctx, model.Plot{
		Code: "P1", SowDate: sow, Variety: "V1", Method: "mean",
	}, "S1")
	must(err)
	day := "2026-03-05"
	// 先建立 seq=1。
	_, err = svc.IngestObservations(ctx, []engine.ObservationInput{
		{StationCode: "S1", Date: day, TMax: 20, TMin: 20, Seq: 1},
	})
	must(err)

	// 20 个并发请求，一半写 seq=2、一半写 seq=3。
	done := make(chan error, 20)
	for i := 0; i < 20; i++ {
		seq := 2
		tmax, tmin := 25.0, 15.0 // seq2: GDD 10
		if i%2 == 0 {
			seq, tmax, tmin = 3, 26, 12 // seq3: GDD 9
		}
		go func() {
			_, e := svc.IngestObservations(ctx, []engine.ObservationInput{
				{StationCode: "S1", Date: day, TMax: tmax, TMin: tmin, Seq: seq},
			})
			done <- e
		}()
	}
	for i := 0; i < 20; i++ {
		must(<-done)
	}

	rows, err := svc.PlotDaily(ctx, "P1", "", "", "")
	must(err)
	dd, _ := time.Parse("2006-01-02", day)
	for _, r := range rows {
		if r.Date.Equal(dd) {
			if r.TMax == nil || *r.TMax != 26 {
				t.Fatalf("并发仲裁后应保留序号 3（tmax=26），got %v", r.TMax)
			}
			if d := r.GDD - 9; d > 1e-9 || d < -1e-9 {
				t.Fatalf("并发仲裁后 GDD 应为 9，got %g", r.GDD)
			}
		}
	}
}
