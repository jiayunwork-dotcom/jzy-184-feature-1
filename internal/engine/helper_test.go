package engine

import (
	"context"
	"testing"
	"time"

	"agristation/internal/enginemem"
	"agristation/internal/model"
)

// 固定“今天”，保证过去/未来划分确定。
var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// testSvc 是绑定内存事务句柄的具体服务类型。
type testSvc = Service[enginemem.Tx]

func newTestService() (*testSvc, *enginemem.Store) {
	m := enginemem.New()
	s := NewService(m)
	s.SetClock(func() time.Time { return testNow })
	return s, m
}

func dstr(d time.Time) string { return d.Format("2006-01-02") }

func seedBase(t *testing.T, s *testSvc) {
	t.Helper()
	ctx := context.Background()
	// 三个站：S1 海拔 500，S2 邻近（约 1.4km）同海拔，S3 远站。
	must(t, s.RegisterStation(ctx, model.Station{Code: "S1", Name: "一号站",
		Latitude: 30.0, Longitude: 100.0, Elevation: 500}))
	must(t, s.RegisterStation(ctx, model.Station{Code: "S2", Name: "二号站",
		Latitude: 30.01, Longitude: 100.01, Elevation: 500}))
	must(t, s.RegisterStation(ctx, model.Station{Code: "S3", Name: "远站",
		Latitude: 32.0, Longitude: 102.0, Elevation: 500}))
	must(t, s.RegisterVariety(ctx, model.Variety{
		Code: "ZD958", Name: "郑单958", BaseTemp: 10, UpperTemp: 30,
		Thresholds: []float64{30, 200, 400, 460, 800},
	}))
}

func seedPlot(t *testing.T, s *testSvc, code, station, method string, sow time.Time) {
	t.Helper()
	_, err := s.RegisterPlot(context.Background(), model.Plot{
		Code: code, Name: code + "号地", SowDate: sow,
		Variety: "ZD958", Method: method,
	}, station)
	must(t, err)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func obs(station, date string, tmax, tmin float64, seq int) ObservationInput {
	return ObservationInput{StationCode: station, Date: date, TMax: tmax, TMin: tmin, Seq: seq}
}

// stagesOf 读取某地块当前阶段结果。
func stagesOf(t *testing.T, s *testSvc, plot string) []model.StageDate {
	t.Helper()
	out, err := s.PlotStages(context.Background(), plot, "")
	must(t, err)
	return out
}

func dailyOf(t *testing.T, s *testSvc, plot string) []model.DailyValue {
	t.Helper()
	out, err := s.PlotDaily(context.Background(), plot, "", "", "")
	must(t, err)
	return out
}

func testNowDate() time.Time {
	return time.Date(testNow.Year(), testNow.Month(), testNow.Day(), 0, 0, 0, 0, time.UTC)
}

func assertDailyEqual(t *testing.T, want, got []model.DailyValue) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("行数不一致：want %d got %d", len(want), len(got))
	}
	for i := range want {
		w, g := want[i], got[i]
		if !w.Date.Equal(g.Date) {
			t.Fatalf("第 %d 行日期不一致：%s vs %s", i, w.Date.Format("01-02"), g.Date.Format("01-02"))
		}
		if d := w.GDD - g.GDD; d > 1e-9 || d < -1e-9 {
			t.Fatalf("%s GDD 不一致：want %g got %g", w.Date.Format("01-02"), w.GDD, g.GDD)
		}
		if d := w.Cumulative - g.Cumulative; d > 1e-9 || d < -1e-9 {
			t.Fatalf("%s 累计不一致：want %g got %g", w.Date.Format("01-02"), w.Cumulative, g.Cumulative)
		}
		if w.Source != g.Source {
			t.Fatalf("%s 来源不一致：want %s got %s", w.Date.Format("01-02"), w.Source, g.Source)
		}
		if w.FillMethod != g.FillMethod {
			t.Fatalf("%s 补值方法不一致：want %s got %s", w.Date.Format("01-02"), w.FillMethod, g.FillMethod)
		}
	}
}
