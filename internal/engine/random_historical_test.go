package engine

import (
	"context"
	"math"
	"math/rand"
	"testing"
	"time"

	"agristation/internal/model"
)

// TestRandomHistoricalParity 随机交错“观测更正/补传、改绑、品种参数修改、
// 历年资料导入与整年替换”，每一步后把集合试走范围与比例与独立的全量
// 参考实现（enginemem.ReferenceOutlook/ReferenceProbability，只用最终
// 数据从头算）逐项核对。
func TestRandomHistoricalParity(t *testing.T) {
	ctx := context.Background()
	asOf := testNowDate() // 2026-06-01
	years := []int{2000, 2001, 2002, 2003, 2004, 2005}
	quantiles := []float64{0.1, 0.5, 0.9}

	for iter := 0; iter < 6; iter++ {
		t.Run(fmtIter(iter), func(t *testing.T) {
			s, m := newTestService()
			seedBase(t, s)
			sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
			method := []string{"sine", "triangle", "mean"}[iter%3]
			seedPlot(t, s, "P1", "S1", method, sow)
			if iter%2 == 0 {
				seedPlot(t, s, "P2", "S2", method, sow.AddDate(0, 0, 2))
			}
			plots := []string{"P1"}
			if iter%2 == 0 {
				plots = append(plots, "P2")
			}
			rng := rand.New(rand.NewSource(int64(iter*7919 + 13)))

			// 先给三个站各导一个基准年，保证任何时候都有资料可试走。
			for _, st := range []string{"S1", "S2", "S3"} {
				if _, err := s.ImportHistorical(ctx, mkYearFixed(st, 1999, 26, 14)); err != nil {
					t.Fatal(err)
				}
			}

			// 随机生成某历史年整年记录（温度按年偏移，制造年际差异）。
			mkYear := func(station string, year int, warm float64) []HistoricalInput {
				d0 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
				days := 365
				if time.Date(year, 2, 29, 0, 0, 0, 0, time.UTC).Day() == 29 {
					days = 366
				}
				recs := make([]HistoricalInput, 0, days)
				for i := 0; i < days; i++ {
					d := d0.AddDate(0, 0, i)
					tmin := 6 + warm + 6*sinDay(i) + rng.Float64()*3
					tmax := tmin + 7 + rng.Float64()*6
					recs = append(recs, HistoricalInput{
						StationCode: station, Year: year,
						Date: d.Format("2006-01-02"), TMax: tmax, TMin: tmin,
					})
				}
				return recs
			}

			check := func(step int) {
				t.Helper()
				for _, plot := range plots {
					got, err := s.PlotOutlook(ctx, plot, dstr(asOf), quantiles)
					want := m.ReferenceOutlook(plot, asOf, quantiles)
					if len(want.Stages) == 0 {
						// 无绑定/无资料：参考器返回空阶段，engine 应给 noHistory。
						if err == nil {
							t.Fatalf("step=%d plot=%s 参考无资料但 engine 给了结果", step, plot)
						}
						continue
					}
					if err != nil {
						t.Fatalf("step=%d plot=%s outlook 出错：%v", step, plot, err)
					}
					assertOutlookEqual(t, step, plot, want, got)

					plotSow := sow
					if plot == "P2" {
						plotSow = sow.AddDate(0, 0, 2)
					}
					// 比例查询每步抽一个阶段、少数几个日期深查
					// （成员年×窗口天的试走较重）。
					st := model.StageOrder[rng.Intn(len(model.StageOrder))]
					for _, off := range []int{0, 40, 90, 270} {
						if off != 0 && off != 90 && rng.Intn(2) == 0 {
							continue
						}
						by := plotSow.AddDate(0, 0, off)
						pr, err := s.PlotStageProbability(ctx, StageProbabilityQuery{
							PlotCode: plot, Stage: string(st),
							By: by.Format("2006-01-02"), QueryDate: dstr(asOf),
						})
						if err != nil {
							t.Fatalf("step=%d 比例查询出错：%v", step, err)
						}
						n, k, status, actual := m.ReferenceProbability(plot, asOf, by, st)
						if pr.Status != status {
							t.Fatalf("step=%d plot=%s %s 状态不一致：%s vs %s",
								step, plot, st, pr.Status, status)
						}
						if status == model.StatusReached {
							if pr.Proportion != float64(k) {
								t.Fatalf("step=%d %s 已达到比例应为 %d，got %g",
									step, st, k, pr.Proportion)
							}
							if (pr.ActualDate == nil) != (actual == nil) ||
								(actual != nil && !pr.ActualDate.Equal(*actual)) {
								t.Fatalf("step=%d %s 实际日不一致：%v vs %v",
									step, st, pr.ActualDate, actual)
							}
							continue
						}
						if n == 0 {
							if pr.NYears != 0 {
								t.Fatalf("step=%d %s 参考无年，engine n=%d", step, st, pr.NYears)
							}
							continue
						}
						if pr.NYears != n || pr.ReachedBy != k {
							t.Fatalf("step=%d plot=%s %s 年数不一致：got (%d/%d) want (%d/%d)",
								step, plot, st, pr.ReachedBy, pr.NYears, k, n)
						}
						wantP := float64(k) / float64(n)
						if d := pr.Proportion - wantP; d > 1e-12 || d < -1e-12 {
							t.Fatalf("step=%d %s 比例不一致：got %g want %g",
								step, st, pr.Proportion, wantP)
						}
					}
				}
			}

			const steps = 120
			maxSeq := map[string]int{}
			for step := 0; step < steps; step++ {
				switch rng.Intn(9) {
				case 0, 1:
					// 导入/替换某站某年（每次内容不同，温度重抽）。
					st := []string{"S1", "S2"}[rng.Intn(2)]
					y := years[rng.Intn(len(years))]
					warm := -3 + rng.Float64()*8
					recs := mkYear(st, y, warm)
					// 偶尔只导一个极短的年（大量缺日，走气候平均/0）。
					if rng.Intn(5) == 0 {
						recs = recs[:3]
					}
					if _, err := s.ImportHistorical(ctx, recs); err != nil {
						t.Fatal(err)
					}
				case 2:
					// 夹一条非法记录：该站-年整体不生效，其余照常。
					st := "S1"
					y := 1998
					recs := mkYear(st, y, 0)
					recs = append(recs, HistoricalInput{
						StationCode: st, Year: y, Date: "1998-13-01", TMax: 20, TMin: 10})
					_, err := s.ImportHistorical(ctx, recs)
					if err != nil {
						t.Fatal(err)
					}
				case 3, 4, 5:
					// 观测更正/补传（过去日）。
					off := rng.Intn(90)
					day := sow.AddDate(0, 0, off)
					if day.After(asOf) {
						day = asOf
					}
					st := []string{"S1", "S2"}[rng.Intn(2)]
					key := st + "|" + dstr(day)
					seq := maxSeq[key] + 1
					maxSeq[key] = seq
					tmin := 5 + rng.Float64()*18
					tmax := tmin + 4 + rng.Float64()*12
					if _, err := s.IngestObservations(ctx, []ObservationInput{
						obs(st, dstr(day), tmax, tmin, seq)}); err != nil {
						t.Fatal(err)
					}
				case 6:
					// 邻站观测，影响补值。
					off := rng.Intn(90)
					day := sow.AddDate(0, 0, off)
					if !day.After(asOf) {
						if _, err := s.IngestObservations(ctx, []ObservationInput{
							obs("S3", dstr(day), 28, 16, maxSeq["S3|"+dstr(day)]+1)}); err != nil {
							t.Fatal(err)
						}
						maxSeq["S3|"+dstr(day)]++
					}
				case 7:
					// 改绑。
					plot := plots[rng.Intn(len(plots))]
					to := "S2"
					if m.ActiveStation(plot, asOf) == "S2" {
						to = "S1"
					}
					plotSow := sow
					if plot == "P2" {
						plotSow = sow.AddDate(0, 0, 2)
					}
					day := plotSow.AddDate(0, 0, rng.Intn(90))
					if err := s.BindPlot(ctx, BindPlotInput{
						PlotCode: plot, StationCode: to,
						EffectiveDate: dstr(day)}); err != nil {
						t.Fatal(err)
					}
				case 8:
					// 品种基点微调（保持严格递增阈值，整体平移基点）。
					base := 6 + float64(rng.Intn(8))
					if err := s.RegisterVariety(ctx, model.Variety{
						Code: "ZD958", BaseTemp: base, UpperTemp: 35,
						Thresholds: []float64{30, 200, 400, 460, 800},
					}); err != nil {
						t.Fatal(err)
					}
				}
				check(step)
			}

			// “重启续跑”后再对一遍：Recover 不得影响集合结果。
			if err := s.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			check(steps)

			// 原有逐日/阶段结果仍与参考逐日一致（新功能不影响旧路径）。
			for _, plot := range plots {
				assertDailyEqual(t, m.ReferenceDaily(plot, asOf), dailyOf(t, s, plot))
			}
		})
	}
}

// sinDay 年周期温度形状（正：夏暖冬冷），入参为年积日。
func sinDay(i int) float64 {
	return math.Sin(float64(i) / 365.0 * 2 * math.Pi)
}

// mkYearFixed 生成恒温（带小幅年周期）的整年历史记录。
func mkYearFixed(station string, year int, tmax, tmin float64) []HistoricalInput {
	d0 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	days := 365
	if time.Date(year, 2, 29, 0, 0, 0, 0, time.UTC).Day() == 29 {
		days = 366
	}
	recs := make([]HistoricalInput, 0, days)
	for i := 0; i < days; i++ {
		d := d0.AddDate(0, 0, i)
		recs = append(recs, HistoricalInput{
			StationCode: station, Year: year,
			Date: d.Format("2006-01-02"),
			TMax: tmax + 2*sinDay(i), TMin: tmin + 2*sinDay(i),
		})
	}
	return recs
}

func assertOutlookEqual(t *testing.T, step int, plot string, want, got *model.Outlook) {
	t.Helper()
	if len(want.Stages) != len(got.Stages) {
		t.Fatalf("step=%d plot=%s 阶段数不一致：%d vs %d",
			step, plot, len(want.Stages), len(got.Stages))
	}
	for i := range want.Stages {
		w, g := want.Stages[i], got.Stages[i]
		if w.Stage != g.Stage || w.Status != g.Status {
			t.Fatalf("step=%d plot=%s 阶段 %s 状态不一致：%s vs %s",
				step, plot, w.Stage, w.Status, g.Status)
		}
		if w.Status == model.StatusReached {
			if (w.ActualDate == nil) != (g.ActualDate == nil) ||
				(w.ActualDate != nil && !w.ActualDate.Equal(*g.ActualDate)) {
				t.Fatalf("step=%d %s 实际日不一致：%v vs %v",
					step, w.Stage, w.ActualDate, g.ActualDate)
			}
			continue
		}
		if w.NYears != g.NYears || w.UnreachedYears != g.UnreachedYears {
			t.Fatalf("step=%d plot=%s %s 年数不一致：want n=%d un=%d got n=%d un=%d",
				step, plot, w.Stage, w.NYears, w.UnreachedYears, g.NYears, g.UnreachedYears)
		}
		if (w.Earliest == nil) != (g.Earliest == nil) {
			t.Fatalf("step=%d %s earliest 有无不一致：%v vs %v", step, w.Stage, w.Earliest, g.Earliest)
		}
		if w.Earliest != nil && !w.Earliest.Equal(*g.Earliest) {
			t.Fatalf("step=%d %s earliest 不一致：%s vs %s",
				step, w.Stage, w.Earliest.Format("01-02"), g.Earliest.Format("01-02"))
		}
		if (w.Latest == nil) != (g.Latest == nil) {
			t.Fatalf("step=%d %s latest 有无不一致", step, w.Stage)
		}
		if w.Latest != nil && !w.Latest.Equal(*g.Latest) {
			t.Fatalf("step=%d %s latest 不一致：%s vs %s",
				step, w.Stage, w.Latest.Format("01-02"), g.Latest.Format("01-02"))
		}
		if len(w.Quantiles) != len(g.Quantiles) {
			t.Fatalf("step=%d %s 分位数不一致", step, w.Stage)
		}
		for j := range w.Quantiles {
			wq, gq := w.Quantiles[j], g.Quantiles[j]
			if wq.Q != gq.Q || wq.Reachable != gq.Reachable {
				t.Fatalf("step=%d %s 分位 %g 可达性不一致：%v vs %v",
					step, w.Stage, wq.Q, wq.Reachable, gq.Reachable)
			}
			if wq.Reachable && (wq.Date == nil || gq.Date == nil ||
				!wq.Date.Equal(*gq.Date)) {
				t.Fatalf("step=%d %s 分位 %g 日期不一致：%v vs %v",
					step, w.Stage, wq.Q, wq.Date, gq.Date)
			}
		}
	}
}
