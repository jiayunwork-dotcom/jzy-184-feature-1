package engine

import (
	"context"
	"math"
	"math/rand"
	"testing"
	"time"

	"agristation/internal/enginemem"
	"agristation/internal/model"
)

// refQuantile 在参考实现的有序到达日上取与 ensemble.Quantile 同口径的分位：
// 第 ceil(q*N) 位（0 起 ceil-1），该位为 nil 表示窗口内到不了。
func refQuantile(ordered []*time.Time, q float64) (*time.Time, bool) {
	n := len(ordered)
	if n == 0 {
		return nil, false
	}
	idx := int(q*float64(n) + 1 - 1e-9)
	if idx < 1 {
		idx = 1
	}
	if idx > n {
		idx = n
	}
	d := ordered[idx-1]
	return d, d != nil
}

func ptrEq(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

// assertEnsembleParity 把服务的范围/比例结果与独立参考实现逐项对账。
func assertEnsembleParity(t *testing.T, s *testSvc, m *enginemem.Store,
	plot string, asOf time.Time, step int) {
	t.Helper()
	qL := []float64{0.0, 0.1, 0.25, 0.5, 0.75, 0.9, 1.0}
	got, err := s.StageRanges(context.Background(), plot, qL, "")
	if err != nil {
		t.Fatalf("step=%d StageRanges 报错：%v", step, err)
	}
	ref := m.ReferenceEnsemble(plot, asOf)

	if got.Available != ref.Available {
		t.Fatalf("step=%d 顶层 available 不一致：got %v want %v（reason=%s）",
			step, got.Available, ref.Available, got.Reason)
	}
	if !got.AsOf.Equal(ref.AsOf) || !got.Horizon.Equal(ref.Horizon) {
		t.Fatalf("step=%d asOf/horizon 不一致", step)
	}
	if len(got.Ranges) != 5 {
		t.Fatalf("step=%d 阶段行数 %d", step, len(got.Ranges))
	}
	if len(ref.Years) != len(got.Ranges[0].Years) {
		// years 以阶段 0 为准（所有阶段共享同一批年份）
	}
	wantYears := ref.Years
	for i, r := range got.Ranges {
		rs := ref.Stages[i]
		if r.Reached != rs.Reached {
			t.Fatalf("step=%d 阶段 %s reached 不一致：got %v want %v",
				step, r.Stage, r.Reached, rs.Reached)
		}
		if !ptrEq(r.ActualDate, rs.Actual) {
			t.Fatalf("step=%d 阶段 %s 实际日不一致：%v vs %v",
				step, r.Stage, r.ActualDate, rs.Actual)
		}
		if r.Reached {
			// 已达到阶段：范围收成实际日。
			if !r.Available || r.Earliest == nil || !r.Earliest.Equal(*rs.Actual) ||
				!r.Latest.Equal(*rs.Actual) {
				t.Fatalf("step=%d 阶段 %s 已达到但范围未收成实际日", step, r.Stage)
			}
			continue
		}
		// 未达到：参考若没有年份，双方都应不可用。
		if len(wantYears) == 0 {
			if r.Available {
				t.Fatalf("step=%d 阶段 %s 无年份却标记可用", step, r.Stage)
			}
			continue
		}
		if !r.Available {
			t.Fatalf("step=%d 阶段 %s 应可用", step, r.Stage)
		}
		if r.YearsUsed != len(wantYears) {
			t.Fatalf("step=%d 阶段 %s 年数不一致：got %d want %d",
				step, r.Stage, r.YearsUsed, len(wantYears))
		}
		for yi, y := range wantYears {
			if r.Years[yi] != y {
				t.Fatalf("step=%d 阶段 %s 年份列表不一致", step, r.Stage)
			}
		}
		// 最早/最晚。
		var wantEarliest, wantLatest *time.Time
		for _, d := range rs.OrderedDates {
			if d != nil {
				if wantEarliest == nil {
					wantEarliest = d
				}
				wantLatest = d
			}
		}
		if !ptrEq(r.Earliest, wantEarliest) || !ptrEq(r.Latest, wantLatest) {
			t.Fatalf("step=%d 阶段 %s 最早最晚不一致：got %v/%v want %v/%v",
				step, r.Stage, r.Earliest, r.Latest, wantEarliest, wantLatest)
		}
		// 未到年数。
		wantNot := 0
		for _, d := range rs.OrderedDates {
			if d == nil {
				wantNot++
			}
		}
		if r.NotReachedYears != wantNot {
			t.Fatalf("step=%d 阶段 %s 未到年数不一致：got %d want %d",
				step, r.Stage, r.NotReachedYears, wantNot)
		}
		// 各分位。
		for _, qp := range r.Quantiles {
			wd, wok := refQuantile(rs.OrderedDates, qp.Quantile)
			if qp.Reachable != wok || !ptrEq(qp.Date, wd) {
				t.Fatalf("step=%d 阶段 %s 分位 %g 不一致：got(%v,%v) want(%v,%v)",
					step, r.Stage, qp.Quantile, qp.Reachable, qp.Date, wok, wd)
			}
		}
	}

	// 比例：沿窗口抽若干天，与参考有序样本逐一核对。
	for i, stage := range model.StageOrder {
		rs := ref.Stages[i]
		// 只在关键偏移抽点（覆盖：窗口初、各分位可能落点、窗口末）。
		for _, off := range []int{0, 5, 20, 45, 80, 120, 170, 220, 269} {
			d := asOf.AddDate(0, 0, off)
			ar, err := s.ArrivalBy(context.Background(), plot, stage,
				d.Format("2006-01-02"), "")
			if err != nil {
				t.Fatalf("step=%d ArrivalBy 报错：%v", step, err)
			}
			if rs.Reached {
				want := 0.0
				if !d.Before(*rs.Actual) {
					want = 1.0
				}
				if math.Abs(ar.Fraction-want) > 1e-12 {
					t.Fatalf("step=%d 已达到阶段 %s off=%d 比例 got %g want %g",
						step, stage, off, ar.Fraction, want)
				}
				continue
			}
			if len(wantYears) == 0 {
				if ar.Available {
					t.Fatalf("step=%d 无年份比例应不可用：阶段 %s", step, stage)
				}
				continue
			}
			k := 0
			for _, dd := range rs.OrderedDates {
				if dd != nil && !dd.After(d) {
					k++
				}
			}
			want := float64(k) / float64(len(wantYears))
			if math.Abs(ar.Fraction-want) > 1e-12 {
				t.Fatalf("step=%d 阶段 %s off=%d 比例不一致：got %g want %g",
					step, stage, off, ar.Fraction, want)
			}
		}
	}
}

// TestRandomEnsembleParity 随机交错：观测更正、改绑、品种参数修改、
// 历年导入与整年替换。每一步之后范围与比例都必须等于“只拿最终数据
// 从头算一遍”（enginemem.ReferenceEnsemble 独立实现）。
func TestRandomEnsembleParity(t *testing.T) {
	ctx := context.Background()
	asOf := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	sow := asOf
	const years = 8

	for iter := 0; iter < 4; iter++ {
		t.Run(fmtIter(iter), func(t *testing.T) {
			s, m := newTestService()
			s.SetClock(func() time.Time { return asOf })
			must(t, s.RegisterStation(ctx, model.Station{Code: "S1", Name: "一",
				Latitude: 30.0, Longitude: 100.0, Elevation: 500}))
			must(t, s.RegisterStation(ctx, model.Station{Code: "S2", Name: "二",
				Latitude: 30.02, Longitude: 100.02, Elevation: 500}))
			must(t, s.RegisterVariety(ctx, model.Variety{
				Code: "V", BaseTemp: 10, UpperTemp: 30,
				Thresholds: []float64{100, 400, 900, 1400, 2400},
			}))
			if _, err := s.RegisterPlot(ctx, model.Plot{
				Code: "P1", SowDate: sow, Variety: "V", Method: "mean",
			}, "S1"); err != nil {
				t.Fatal(err)
			}

			rng := rand.New(rand.NewSource(int64(iter*7919 + 13)))

			// 气候平均（本站）：随日序变化，保证缺历史时能补、且未来外推有值。
			seedNormalsAll(t, s, "S1", func(doy int) (float64, float64) {
				tmax := 21 + 4*math.Sin(float64(doy)*0.025)
				return tmax, tmax - 8
			})
			seedNormalsAll(t, s, "S2", func(doy int) (float64, float64) {
				tmax := 20 + 4*math.Cos(float64(doy)*0.025)
				return tmax, tmax - 8
			})

			// 初始导入 years 个历史年（S1、S2 都要，改绑后候选年取交集）。
			randomYear := func(_ int) (float64, float64) {
				g := 6.0 + rng.Float64()*14 // mean GDD 6..20
				return 10 + g + 4, 10 + g - 4
			}
			// 初始导入 years 个历史年（S1、S2 都要，改绑后候选年取交集）。
			for y := 0; y < years; y++ {
				calY := 2000 + y
				tmax0, tmin0 := randomYear(calY)
				fn := func(time.Time) (float64, float64) { return tmax0, tmin0 }
				seedHistoricalYear(t, s, "S1", calY, fn)
				seedHistoricalYear(t, s, "S2", calY, fn)
			}

			assertEnsembleParity(t, s, m, "P1", asOf, -1)

			histYearInput := func(station string, calY int,
				tmax, tmin float64) []HistoricalInput {
				in := make([]HistoricalInput, 0, 365)
				for d := time.Date(calY, 1, 1, 0, 0, 0, 0, time.UTC); d.Year() == calY; d = d.AddDate(0, 0, 1) {
					in = append(in, histOne(station, d.Format("2006-01-02"), tmax, tmin))
				}
				return in
			}

			const steps = 80
			for step := 0; step < steps; step++ {
				switch rng.Intn(7) {
				case 0, 1:
					// 当季观测（asOf 当天；asOf==sow，只可能落在播种日）。
					// 用稍晚的固定 asOf 场景里没有历史观测，因此这里改为
					// 在播种日补一条观测也可；主要扰动来自其他操作。
					tmin := 8 + rng.Float64()*10
					_, err := s.IngestObservations(ctx, []ObservationInput{
						obs("S1", asOf.Format("2006-01-02"), tmin+10, tmin, int(step)+1),
					})
					must(t, err)
				case 2:
					// 改绑到 S2（自播种日起），再以同样概率改回。
					to := "S2"
					if rng.Intn(2) == 0 {
						to = "S1"
					}
					_ = s.BindPlot(ctx, BindPlotInput{
						PlotCode: "P1", StationCode: to,
						EffectiveDate: sow.Format("2006-01-02"),
					})
				case 3:
					// 品种参数修改：只改阈值（基点单调测试另守），保持严格递增。
					base := []float64{50, 200, 600, 1000, 2000}
					for k := range base {
						base[k] += rng.Float64() * 200
					}
					// 保证严格递增。
					for k := 1; k < len(base); k++ {
						if base[k] <= base[k-1] {
							base[k] = base[k-1] + 50
						}
					}
					must(t, s.RegisterVariety(ctx, model.Variety{
						Code: "V", BaseTemp: 10, UpperTemp: 30, Thresholds: base,
					}))
				case 4, 5:
					// 历年整年替换：随机挑一年，换成新的恒温。
					calY := 2000 + rng.Intn(years)
					g := 6.0 + rng.Float64()*14
					tmax, tmin := 10+g+4, 10+g-4
					station := "S1"
					if rng.Intn(2) == 0 {
						station = "S2"
					}
					_, err := s.IngestHistorical(ctx, histYearInput(station, calY, tmax, tmin))
					must(t, err)
				case 6:
					// 气候平均变更（影响缺历史补值与未来外推）。
					doy := 1 + rng.Intn(366)
					tmax := 20 + rng.Float64()*6
					must(t, s.PutClimateNormal(ctx, model.ClimateNormal{
						StationCode: "S1", DOY: doy, TMax: tmax, TMin: tmax - 8,
					}))
				}
				assertEnsembleParity(t, s, m, "P1", asOf, step)
			}
		})
	}
}
