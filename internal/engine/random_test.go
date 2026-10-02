package engine

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

// TestRandomCorrectionsIncrementalVsFull 是核心一致性测试：
// 在大量随机乱序、补传、更正、改绑、邻站补值交错进行之后，增量维护的
// 逐日结果必须与“只拿最终数据从播种日从头算一遍”逐日相同。
//
// 每一步后都比对一次（而不仅是最后），以抓出“中途漂移但碰巧收敛”的问题。
func TestRandomCorrectionsIncrementalVsFull(t *testing.T) {
	ctx := context.Background()
	sow := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	const days = 70

	for iter := 0; iter < 12; iter++ {
		t.Run(fmtIter(iter), func(t *testing.T) {
			s, m := newTestService()
			seedBase(t, s)
			method := []string{"sine", "triangle", "mean"}[iter%3]
			seedPlot(t, s, "P1", "S1", method, sow)
			if iter%2 == 0 {
				seedPlot(t, s, "P2", "S3", "sine", sow.AddDate(0, 0, 3))
			}

			rng := rand.New(rand.NewSource(int64(iter*1000 + 7)))
			plots := []string{"P1"}
			if iter%2 == 0 {
				plots = append(plots, "P2")
			}

			// 已经写入过的站日的当前最大序号（用于随机生成更新/更正）。
			maxSeq := map[string]int{}
			stationFor := func(plot string, day time.Time) string {
				if st := m.ActiveStation(plot, day); st != "" {
					return st
				}
				if plot == "P2" {
					return "S3"
				}
				return "S1"
			}

			temps := func() (tmax, tmin float64) {
				tmin = 2 + rng.Float64()*20
				tmax = tmin + 3 + rng.Float64()*18
				return
			}

			const steps = 220
			for step := 0; step < steps; step++ {
				plot := plots[rng.Intn(len(plots))]
				dayOffset := rng.Intn(days)
				day := sow.AddDate(0, 0, dayOffset)
				if day.After(testNow) {
					day = sow.AddDate(0, 0, rng.Intn(50)) // 未来日观测也允许（补传场景）
					if day.After(testNow) {
						day = testNow
					}
				}
				stCode := stationFor(plot, day)
				key := stCode + "|" + dstr(day)
				cur := maxSeq[key]

				switch rng.Intn(10) {
				case 0, 1, 2:
					// 更大序号更正/补传。
					seq := cur + 1
					tmax, tmin := temps()
					_, err := s.IngestObservations(ctx, []ObservationInput{
						obs(stCode, dstr(day), tmax, tmin, seq)})
					must(t, err)
					maxSeq[key] = seq
				case 3:
					// 乱序晚到的小序号：合法但不生效。
					seq := cur
					if seq < 1 {
						seq = 1
					}
					_, err := s.IngestObservations(ctx, []ObservationInput{
						obs(stCode, dstr(day), 35, 25, seq)})
					must(t, err)
				case 4:
					// 邻站同日上报，制造/改变补值。
					donor := "S2"
					if stCode == "S2" {
						donor = "S1"
					}
					tmax, tmin := temps()
					_, err := s.IngestObservations(ctx, []ObservationInput{
						obs(donor, dstr(day), tmax, tmin, 1)})
					must(t, err)
				case 5:
					// 同站日重复补传（相同序号相同内容）。
					if cur >= 1 {
						tmax, tmin := temps()
						_, err := s.IngestObservations(ctx, []ObservationInput{
							obs(stCode, dstr(day), tmax, tmin, cur)})
						must(t, err)
					}
				case 6:
					// 改绑到邻近站（约一成步数）。
					if plot == "P1" && dayOffset >= 1 {
						to := "S2"
						if stCode == "S2" {
							to = "S1"
						}
						must(t, s.BindPlot(ctx, BindPlotInput{
							PlotCode: plot, StationCode: to, EffectiveDate: dstr(day),
						}))
					}
				case 7:
					// 批量一次写多个站日。
					batch := make([]ObservationInput, 0, 5)
					for k := 0; k < 5; k++ {
						off := rng.Intn(days)
						d2 := sow.AddDate(0, 0, off)
						if d2.After(testNow) {
							continue
						}
						s2 := stationFor(plot, d2)
						k2 := s2 + "|" + dstr(d2)
						seq := maxSeq[k2] + 1
						maxSeq[k2] = seq
						tmax, tmin := temps()
						batch = append(batch, obs(s2, dstr(d2), tmax, tmin, seq))
					}
					if len(batch) > 0 {
						_, err := s.IngestObservations(ctx, batch)
						must(t, err)
					}
				default:
					// 8、9：什么都不做，让中间状态被对比。
				}

				// 每个被维护的地块都与全量重算逐日对比。
				asOf := testNowDate()
				want := m.ReferenceDaily(plot, asOf)
				got := dailyOf(t, s, plot)
				assertDailyEqual(t, want, got)

				// 阶段日期同样一致。
				wst := m.ReferenceStages(plot, asOf)
				gst := stagesOf(t, s, plot)
				if len(wst) != len(gst) {
					t.Fatalf("阶段数不一致：%d vs %d", len(wst), len(gst))
				}
				for i := range wst {
					if (wst[i].Date == nil) != (gst[i].Date == nil) {
						t.Fatalf("step=%d plot=%s 阶段 %s 日期有无不一致：want=%v got=%v",
							step, plot, wst[i].Stage, wst[i].Date, gst[i].Date)
					}
					if wst[i].Date != nil && !wst[i].Date.Equal(*gst[i].Date) {
						t.Fatalf("step=%d plot=%s 阶段 %s 日期不一致：want %s got %s",
							step, plot, wst[i].Stage,
							wst[i].Date.Format("01-02"), gst[i].Date.Format("01-02"))
					}
					if wst[i].Status != gst[i].Status {
						t.Fatalf("step=%d plot=%s 阶段 %s 状态不一致：want %s got %s",
							step, plot, wst[i].Stage, wst[i].Status, gst[i].Status)
					}
				}
			}
		})
	}
}

func fmtIter(i int) string {
	return "iter-" + itoa(i)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
