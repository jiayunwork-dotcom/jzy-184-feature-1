// Package gdd 实现从日最高、最低气温计算日积温（Growing Degree Day）的几种口径。
//
// 三种口径：
//   - mean：直接取 (tmax+tmin)/2 - base，下限截断为 0，不做上限截断
//     （题目给出的“直接取平均减基点”口径）。
//   - sine：单正弦近似。假设温度过程关于半日呈正弦：白天段从 tmin 升到 tmax，
//     夜间段从 tmax 落回 tmin。逐时取 clip(T(t), base, upper) 后对一日积分，
//     再除以一日“弧度长度”（2π）。解析解采用 Baskerville & Emin (1969)
//     的 Ecological Modelling 经典形式并扩展到上限截断。
//   - triangle：单三角近似。与正弦同样的分段，只是把曲线换成两段直线，
//     解析积分更简单。
//
// 共同保证（见 gdd_test.go）：
//  1. 最高最低都落在 [base, upper] 内时，三种口径都等于平均温度减基点；
//  2. tmax <= base 时结果为 0；
//  3. 任何输入结果不为负；
//  4. base 调高，结果只减不增（因此阶段日期只会推迟或不变）。
package gdd

import (
	"errors"
	"math"
)

// Method 日积温口径标识。
type Method string

const (
	MethodMean     Method = "mean"
	MethodSine     Method = "sine"
	MethodTriangle Method = "triangle"
)

// DefaultMethod 是全站默认口径：单正弦双截断，理由见包注释与 docs/design.md。
const DefaultMethod = MethodSine

// ParseMethod 解析口径标识，空串按默认口径处理。
func ParseMethod(s string) (Method, error) {
	switch Method(s) {
	case "":
		return DefaultMethod, nil
	case MethodMean, MethodSine, MethodTriangle:
		return Method(s), nil
	default:
		return "", errors.New("unknown gdd method: " + s)
	}
}

const (
	eps = 1e-9
)

// Daily 按指定口径计算日积温。要求 tmin <= tmax、base < upper，
// 参数合法性由上层（品种/温度校验）保证；这里对退化情况做稳妥处理。
func Daily(method Method, tmax, tmin, base, upper float64) float64 {
	// 最高温不超过基点：全天不超过基点，积温为零。
	if tmax <= base+eps {
		return 0
	}
	// 退化日（最高==最低）：按恒温处理。
	if tmax-tmin < eps {
		return clip(tmax, base, upper) - base
	}
	var g float64
	switch method {
	case MethodMean:
		g = meanGDD(tmax, tmin, base)
	case MethodTriangle:
		g = triangleGDD(tmax, tmin, base, upper)
	default:
		g = sineGDD(tmax, tmin, base, upper)
	}
	if g < 0 {
		return 0
	}
	return g
}

// clip 把温度截断到 [base, upper]。
func clip(t, base, upper float64) float64 {
	if t < base {
		return base
	}
	if t > upper {
		return upper
	}
	return t
}

// meanGDD 平均法：max((tmax+tmin)/2 - base, 0)。
// 按题面定义不做上限截断。
func meanGDD(tmax, tmin, base float64) float64 {
	m := (tmax + tmin) / 2
	if m <= base {
		return 0
	}
	return m - base
}

// sineGDD 单正弦、基点与上限双截断的日积温解析解。
//
// 温度过程写成 T(t) = m + a * sin(t - π/2)，t∈[0, 2π)，
// 其中 m=(tmax+tmin)/2、a=(tmax-tmin)/2：t=π/2 处取 tmax，
// t=0、2π 处取 tmin。对 clip(T, base, upper)-base 积分后除以 2π。
//
// 记 phi(x) = asin(clamp((x-m)/a, -1, 1))，L(x)=1-(x-m)/a。
// 跨阈值 x 的一个“波峰区间”贡献为
//
//	1/2 * (1/π) * [ (m-x)*π/2 + (m-x)*phi(x) + a*cos(phi(x)) ]
//
// = ((m-x)/2)(1/2 + phi/π) + a/(2π)*cos(phi)
//
// 最终：有上限时减去上限以上部分的“超额积分”。
func sineGDD(tmax, tmin, base, upper float64) float64 {
	m := (tmax + tmin) / 2
	a := (tmax - tmin) / 2
	if a < eps {
		// 恒温日。
		return clip(tmax, base, upper) - base
	}

	// base < tmax 已由 Daily 保证。
	g := sineExcessAbove(m, a, base)
	if upper < tmax {
		g -= sineExcessAbove(m, a, upper)
	}
	if g < 0 {
		return 0
	}
	return g
}

// sineExcessAbove 返回 T(t) 高于阈值 c 的部分（T-c）在一日上的平均，
// 即 ∫max(T-c,0) dt / 2π。
//
// 令 u=t-π/2，高于 c 的区间为 u∈(φ, π-φ)，φ=asin((c-m)/a)，
// 积分解析结果为：
//
//	(m-c)(1/2 - φ/π) + a·cos(φ)/π
func sineExcessAbove(m, a, c float64) float64 {
	if m+a <= c+eps {
		return 0
	}
	if m-a >= c {
		// 全天高于阈值。
		return m - c
	}
	x := (c - m) / a
	if x < -1 {
		x = -1
	} else if x > 1 {
		x = 1
	}
	phi := math.Asin(x)
	return (m-c)/2 - (m-c)*phi/math.Pi + a*math.Cos(phi)/math.Pi
}

// triangleGDD 单三角近似：0..π 由 tmin 线性升到 tmax，π..2π 落回，
// 逐时 clip 到 [base, upper] 后积分取日平均。
func triangleGDD(tmax, tmin, base, upper float64) float64 {
	a := (tmax - tmin) / 2
	if a < eps {
		return clip(tmax, base, upper) - base
	}
	m := (tmax + tmin) / 2

	// 上升支 [0,π]：T=m-a+(2a/π)t；下降支 [π,2π] 对称。
	// 两支对“高于 c 的部分”的日平均贡献相同，计算一支再合并。
	// 一支上 ∫max(T-c,0)dt / 2π，两支合计 = 一支积分/π。
	above := func(c float64) float64 {
		if tmax <= c+eps {
			return 0
		}
		if tmin >= c {
			// 一支全部在 c 以上，一支积分 = (tmax+tmin)/2 - c 乘 π。
			return m - c
		}
		// 交点 t0 = π*(c-(m-a))/(2a)，[t0,π] 为三角形，
		// 一支积分 = 1/2*(π-t0)*(tmax-c)。
		t0 := math.Pi * (c - (m - a)) / (2 * a)
		area := 0.5 * (math.Pi - t0) * (tmax - c)
		return area / math.Pi // 两支合计 / 2π 后正好等于一支积分/π
	}

	g := above(base)
	if upper < tmax {
		g -= above(upper)
	}
	if g < 0 {
		return 0
	}
	return g
}
