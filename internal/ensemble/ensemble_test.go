package ensemble

import (
	"testing"
	"time"
)

func d(day int) time.Time {
	return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC)
}

func arrivals(items ...struct {
	y       int
	reached bool
	day     int
}) []Arrival {
	out := make([]Arrival, 0, len(items))
	for _, it := range items {
		a := Arrival{Year: it.y, Reached: it.reached}
		if it.reached {
			a.Date = d(it.day)
		}
		out = append(out, a)
	}
	return out
}

func TestOrderedArrivals(t *testing.T) {
	in := arrivals(
		struct {
			y       int
			reached bool
			day     int
		}{3, false, 0},
		struct {
			y       int
			reached bool
			day     int
		}{1, true, 20},
		struct {
			y       int
			reached bool
			day     int
		}{2, true, 10},
	)
	out := OrderedArrivals(in)
	if !out[0].Reached || out[0].Date.Day() != 10 {
		t.Fatalf("最早到达应排第一：%+v", out[0])
	}
	if !out[1].Reached || out[1].Date.Day() != 20 {
		t.Fatalf("其次到达：%+v", out[1])
	}
	if out[2].Reached {
		t.Fatalf("未到年应排最后")
	}
}

func TestQuantileOrderStatistic(t *testing.T) {
	in := OrderedArrivals(arrivals(
		struct {
			y       int
			reached bool
			day     int
		}{1, true, 1},
		struct {
			y       int
			reached bool
			day     int
		}{2, true, 2},
		struct {
			y       int
			reached bool
			day     int
		}{3, true, 3},
		struct {
			y       int
			reached bool
			day     int
		}{4, true, 4},
		struct {
			y       int
			reached bool
			day     int
		}{5, true, 5},
		struct {
			y       int
			reached bool
			day     int
		}{6, true, 6},
		struct {
			y       int
			reached bool
			day     int
		}{7, true, 7},
		struct {
			y       int
			reached bool
			day     int
		}{8, true, 8},
		struct {
			y       int
			reached bool
			day     int
		}{9, true, 9},
		struct {
			y       int
			reached bool
			day     int
		}{10, false, 0},
	))
	// 10 个样本：0.1 取下标 0（3/1），0.5 下标 4（3/5），0.9 下标 8（3/9），
	// 1.0 下标 9 是未到年 → 不可达。
	cases := []struct {
		q         float64
		wantDay   int
		reachable bool
	}{
		{0.0, 1, true},
		{0.1, 1, true},
		{0.5, 5, true},
		{0.9, 9, true},
		{0.95, 10, false}, // ceil(9.5)=10 → 第 10 位未到
		{1.0, 0, false},
	}
	for _, c := range cases {
		date, reach := Quantile(in, c.q)
		if reach != c.reachable {
			t.Fatalf("q=%g reachable got %v want %v", c.q, reach, c.reachable)
		}
		if reach && date.Day() != c.wantDay {
			t.Fatalf("q=%g day got %d want %d", c.q, date.Day(), c.wantDay)
		}
	}
	if n := NotReachedCount(in); n != 1 {
		t.Fatalf("未到年数应为 1，got %d", n)
	}
	e, l := Earliest(in), Latest(in)
	if e == nil || e.Day() != 1 || l == nil || l.Day() != 9 {
		t.Fatalf("最早/最晚不对：%v %v", e, l)
	}
}

func TestFractionBy(t *testing.T) {
	in := OrderedArrivals(arrivals(
		struct {
			y       int
			reached bool
			day     int
		}{1, true, 2},
		struct {
			y       int
			reached bool
			day     int
		}{2, true, 4},
		struct {
			y       int
			reached bool
			day     int
		}{3, true, 6},
		struct {
			y       int
			reached bool
			day     int
		}{4, false, 0},
	))
	if f := FractionBy(in, d(1)); f != 0 {
		t.Fatalf("第一天前应为 0，got %g", f)
	}
	if f := FractionBy(in, d(2)); f != 0.25 {
		t.Fatalf("3/2 当天应 1/4，got %g", f)
	}
	if f := FractionBy(in, d(5)); f != 0.5 {
		t.Fatalf("3/5 应 2/4，got %g", f)
	}
	if f := FractionBy(in, d(6)); f != 0.75 {
		t.Fatalf("3/6 应 3/4，got %g", f)
	}
	if f := FractionBy(in, d(30)); f != 0.75 {
		t.Fatalf("窗口末未到年不计数，应仍 0.75，got %g", f)
	}
}

func TestYearEligibility(t *testing.T) {
	// 一段全由气候平均（Eligible=true, Observed=false）构成：失格。
	days1 := []FutureDay{
		{Date: d(1), Eligible: true, Observed: false, Segment: 0},
		{Date: d(2), Eligible: true, Observed: false, Segment: 0},
	}
	if ok, _ := YearEligible(days1); ok {
		t.Fatal("段内无真实历史日应失格")
	}
	// 同段至少一个真实日：合格。
	days2 := []FutureDay{
		{Date: d(1), Eligible: true, Observed: false, Segment: 0},
		{Date: d(2), Eligible: true, Observed: true, Segment: 0},
	}
	if ok, _ := YearEligible(days2); !ok {
		t.Fatal("段内有真实日应合格")
	}
	// 有一天历史与气候平均都缺：失格。
	days3 := []FutureDay{
		{Date: d(1), Eligible: true, Observed: true, Segment: 0},
		{Date: d(2), Eligible: false, Segment: 0},
	}
	if ok, _ := YearEligible(days3); ok {
		t.Fatal("存在双重缺测日应失格")
	}
	// 第二段无真实日：失格（改绑场景）。
	days4 := []FutureDay{
		{Date: d(1), Eligible: true, Observed: true, Segment: 0},
		{Date: d(2), Eligible: true, Observed: false, Segment: 1},
	}
	if ok, _ := YearEligible(days4); ok {
		t.Fatal("无真实日的绑定段应失格")
	}
	// 无绑定日（Zero）不参与判定。
	days5 := []FutureDay{
		{Date: d(1), Eligible: true, Observed: true, Segment: 0},
		{Date: d(2), Zero: true, Segment: 1},
	}
	if ok, _ := YearEligible(days5); !ok {
		t.Fatal("无绑定日不应导致失格")
	}
}

func TestQuantileMonotonicAcrossStagesThresholds(t *testing.T) {
	// 基点调高 GDD 只减不增由 gdd 包保证；这里验证同一序列上阈值变大
	// 到达日只推迟或不到。
	days := make([]FutureDay, 0, 10)
	for i := 1; i <= 10; i++ {
		days = append(days, FutureDay{Date: d(i), Eligible: true, Observed: true,
			TMax: 20, TMin: 20, Segment: 0})
	}
	cfg := Config{BaseTemp: 10, UpperTemp: 30, Thresholds: []float64{10, 50, 90, 100, 101}}
	r := SimulateYear(YearInput{Year: 2000, Days: days}, cfg)
	// mean GDD=10/天：10 在 3/1，50 在 3/5，90 在 3/9，100 在 3/10，
	// 101 窗口内到不了。
	want := []struct {
		reached bool
		day     int
	}{{true, 1}, {true, 5}, {true, 9}, {true, 10}, {false, 0}}
	for i := range want {
		if r[i].Reached != want[i].reached {
			t.Fatalf("阈值 %g 到达状态 got %v want %v", cfg.Thresholds[i], r[i].Reached, want[i].reached)
		}
		if r[i].Reached && r[i].Date.Day() != want[i].day {
			t.Fatalf("阈值 %g 日期 got %d want %d", cfg.Thresholds[i], r[i].Date.Day(), want[i].day)
		}
	}
}
