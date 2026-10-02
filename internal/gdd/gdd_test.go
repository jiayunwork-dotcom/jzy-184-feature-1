package gdd

import (
	"math"
	"testing"
)

func approxEq(t *testing.T, got, want float64, msg string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s: got %.6f want %.6f", msg, got, want)
	}
}

// TestExample12 题目给定算例：基点 10、最高 30、最低 14，当日积温 12。
// 最高最低都在基点与上限之间时，三种口径都必须等于平均温度减基点。
func TestExample12(t *testing.T) {
	for _, m := range []Method{MethodMean, MethodSine, MethodTriangle} {
		got := Daily(m, 30, 14, 10, 35)
		approxEq(t, got, 12, "方法 "+string(m))
	}
}

// TestInteriorEqualsMean 多组完全落在区间内的温度，三种口径应一致为均值减基点。
func TestInteriorEqualsMean(t *testing.T) {
	cases := []struct{ tmax, tmin, base, upper float64 }{
		{25, 15, 10, 35},
		{20, 11, 10, 40},
		{30, 30, 10, 35}, // 恒温日
		{12, 10.5, 10, 40},
	}
	for _, c := range cases {
		want := (c.tmax+c.tmin)/2 - c.base
		for _, m := range []Method{MethodMean, MethodSine, MethodTriangle} {
			got := Daily(m, c.tmax, c.tmin, c.base, c.upper)
			if math.Abs(got-want) > 1e-9 {
				t.Errorf("%v (%g,%g base=%g): got %.6f want %.6f",
					m, c.tmax, c.tmin, c.base, got, want)
			}
		}
	}
}

// TestZeroWhenTmaxAtOrBelowBase 最高温不超过基点时为零。
func TestZeroWhenTmaxAtOrBelowBase(t *testing.T) {
	cases := []struct{ tmax, tmin float64 }{
		{10, -5}, {10, 10}, {9.9, -20}, {0, -10},
	}
	for _, c := range cases {
		for _, m := range []Method{MethodMean, MethodSine, MethodTriangle} {
			got := Daily(m, c.tmax, c.tmin, 10, 35)
			if got != 0 {
				t.Errorf("%v (%g,%g): 应为 0，got %.6f", m, c.tmax, c.tmin, got)
			}
		}
	}
}

// TestNeverNegative 随机极端输入下永不为负。
func TestNeverNegative(t *testing.T) {
	for tmax := -50.0; tmax <= 60; tmax += 1.5 {
		for tmin := -55.0; tmin <= tmax; tmin += 3.7 {
			for _, base := range []float64{0, 5, 10, 15} {
				for _, upper := range []float64{25, 30, 35} {
					for _, m := range []Method{MethodMean, MethodSine, MethodTriangle} {
						if g := Daily(m, tmax, tmin, base, upper); g < -1e-12 {
							t.Fatalf("%v 出现负积温 %.6f: tmax=%g tmin=%g base=%g upper=%g",
								m, g, tmax, tmin, base, upper)
						}
					}
				}
			}
		}
	}
}

// TestBaseMonotonicity 基点调高，日积温只减不增（保证阶段日期只推迟或不变）。
func TestBaseMonotonicity(t *testing.T) {
	for tmax := -10.0; tmax <= 45; tmax += 2.3 {
		for tmin := -15.0; tmin <= tmax; tmin += 4.1 {
			for _, m := range []Method{MethodMean, MethodSine, MethodTriangle} {
				prev := math.Inf(1)
				for base := -5.0; base < 30; base += 0.5 {
					g := Daily(m, tmax, tmin, base, 40)
					if g > prev+1e-9 {
						t.Fatalf("%v 基点从更低升到 %g 时积温反而增大：%g -> %g (%g,%g)",
							m, base, prev, g, tmax, tmin)
					}
					prev = g
				}
			}
		}
	}
}

// TestUpperCapping 当温度超过上限时，双截断口径应小于等于平均法。
func TestUpperCapping(t *testing.T) {
	// 最高 40、最低 20，上限 30：平均法给 20，截断后应明显更小。
	mean := Daily(MethodMean, 40, 20, 10, 30)
	sine := Daily(MethodSine, 40, 20, 10, 30)
	tri := Daily(MethodTriangle, 40, 20, 10, 30)
	if !(sine < mean && tri < mean) {
		t.Fatalf("上限截断未生效：mean=%g sine=%g triangle=%g", mean, sine, tri)
	}
	// 恒温 35、上限 30：截断为 30-10=20。
	approxEq(t, Daily(MethodSine, 35, 35, 10, 30), 20, "恒温上限截断 sine")
	approxEq(t, Daily(MethodTriangle, 35, 35, 10, 30), 20, "恒温上限截断 triangle")
	// 全天高于上限：sine=upper-base=20。
	approxEq(t, Daily(MethodSine, 42, 32, 10, 30), 20, "全天超上限 sine")
	approxEq(t, Daily(MethodTriangle, 42, 32, 10, 30), 20, "全天超上限 triangle")
}

// TestSineVsMeanDifference 验证默认口径与平均法的差异方向：
// 最低温低于基点（或最高温超过上限）的日子差异最大。
func TestSineVsMeanDifference(t *testing.T) {
	// 最低 5 < 基点 10，最高 25：平均法给 (25+5)/2-10=5；
	// 正弦法只积分高于基点部分，应大于平均法（平均法被夜间低温“扣掉”了热量）。
	mean := Daily(MethodMean, 25, 5, 10, 35)
	sine := Daily(MethodSine, 25, 5, 10, 35)
	tri := Daily(MethodTriangle, 25, 5, 10, 35)
	approxEq(t, mean, 5, "平均法")
	if !(sine > mean && tri > mean) {
		t.Fatalf("低基点穿越日正弦/三角应大于平均法：mean=%g sine=%g tri=%g", mean, sine, tri)
	}
	// 全部落在区间内：无差异。
	approxEq(t, Daily(MethodSine, 25, 15, 10, 35), Daily(MethodMean, 25, 15, 10, 35),
		"区间内 sine==mean")
}

// TestParseMethod 口径解析默认值与非法值。
func TestParseMethod(t *testing.T) {
	m, err := ParseMethod("")
	if err != nil || m != MethodSine {
		t.Fatalf("空串应解析为默认 sine，got %q %v", m, err)
	}
	if _, err := ParseMethod("bogus"); err == nil {
		t.Fatal("非法口径应报错")
	}
}
