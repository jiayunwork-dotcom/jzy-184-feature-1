package cum

import (
	"testing"

	"agristation/internal/gdd"
	"agristation/internal/model"
)

func testVariety() *model.Variety {
	return &model.Variety{
		Code:       "v1",
		BaseTemp:   10,
		UpperTemp:  30,
		Thresholds: []float64{20, 100, 200, 250, 400},
	}
}

func mkDay(offset int, gdd float64, src model.Source) Day {
	return Day{Date: model.DateFrom(2026, 4, 1+offset), GDD: gdd, Source: src}
}

// TestAccumulateAndStages 逐日累计与阶段首次达到判定。
func TestAccumulateAndStages(t *testing.T) {
	v := testVariety()
	days := make([]Day, 0, 30)
	for i := 0; i < 30; i++ {
		days = append(days, mkDay(i, 10, model.SourceObserved))
	}
	acc := Accumulate(Series{PlotCode: "p1", Variety: v, Method: gdd.MethodSine, Days: days})
	if acc[0].Cumulative != 10 || acc[9].Cumulative != 100 {
		t.Fatalf("累计错误：%g %g", acc[0].Cumulative, acc[9].Cumulative)
	}
	asOf := model.DateFrom(2026, 4, 30)
	res := Stages(acc, v, asOf)
	// 出苗：累计在 offset1 = 20（4/2）
	if res[0].Date == nil || res[0].Date.Day() != 2 || res[0].Status != model.StatusReached {
		t.Fatalf("出苗日期/状态错误：%+v", res[0])
	}
	if res[1].Date == nil || res[1].Date.Day() != 10 {
		t.Fatalf("拔节应在 offset9（4/10）达到：%+v", res[1].Date)
	}
	if res[2].Date == nil || res[2].Date.Day() != 20 {
		t.Fatalf("抽雄应在 offset19（4/20）达到：%+v", res[2].Date)
	}
	// 成熟 400 在 30 天（累计 300）内达不到：日期为空。
	if res[4].Date != nil || res[4].Status != "" {
		t.Fatalf("未达到的阶段应为空：%+v", res[4])
	}
}

// TestReachedVsForecast asOf 之前达到为 reached，之后（靠气候外推）为 forecast。
func TestReachedVsForecast(t *testing.T) {
	v := testVariety()
	days := make([]Day, 0, 30)
	for i := 0; i < 30; i++ {
		src := model.SourceObserved
		if i >= 10 {
			src = model.SourceClimate
		}
		days = append(days, mkDay(i, 10, src))
	}
	acc := Accumulate(Series{Variety: v, Days: days})
	asOf := model.DateFrom(2026, 4, 11) // offset 10
	res := Stages(acc, v, asOf)
	// 拔节在 offset9（4/10）<= asOf：reached
	if res[1].Status != model.StatusReached {
		t.Fatalf("拔节应为 reached：%+v", res[1])
	}
	// 抽雄在 offset19（4/20）> asOf：forecast
	if res[2].Status != model.StatusForecast {
		t.Fatalf("抽雄应为 forecast：%+v", res[2])
	}
}
