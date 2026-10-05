package outlook

import (
	"math"
	"testing"
)

func TestQuantileAndRange(t *testing.T) {
	// 10 个年份：8 年在第 10..80 天（步长 10）达到，2 年窗口内没到。
	mems := make([]Member, 0, 10)
	for i := 1; i <= 8; i++ {
		mems = append(mems, Member{Year: 2000 + i, Reached: true, Days: i * 10})
	}
	mems = append(mems, Member{Year: 2009, Reached: false}, Member{Year: 2010, Reached: false})
	d := NewDistribution(mems)

	if d.N() != 10 || d.Unreached() != 2 {
		t.Fatalf("N/未到年数错：%d %d", d.N(), d.Unreached())
	}
	if e, ok := d.Earliest(); !ok || e != 10 {
		t.Fatalf("最早应为第 10 天：%d %v", e, ok)
	}
	if l, ok := d.Latest(); !ok || l != 80 {
		t.Fatalf("最晚（只看达到的年）应为第 80 天：%d %v", l, ok)
	}

	// r=ceil(0.1*10)=1 -> 第 10 天；0.5 -> r=5 -> 50；0.9 -> r=9 -> 落在没到年。
	if v, ok, err := d.Quantile(0.1); err != nil || !ok || v != 10 {
		t.Fatalf("q=0.1 应为第 10 天：v=%d ok=%v err=%v", v, ok, err)
	}
	if v, ok, err := d.Quantile(0.5); err != nil || !ok || v != 50 {
		t.Fatalf("q=0.5 应为第 50 天：v=%d ok=%v err=%v", v, ok, err)
	}
	if ok, err := func() (bool, error) {
		_, reachable, e := d.Quantile(0.9)
		return reachable, e
	}(); err != nil || ok {
		t.Fatalf("q=0.9 应如实报窗口内到不了：reachable=%v err=%v", ok, err)
	}
	// q=0 取最早；q=1 落在没到尾部。
	if v, ok, _ := d.Quantile(0); !ok || v != 10 {
		t.Fatalf("q=0 应为最早：%d %v", v, ok)
	}
	if _, reachable, _ := d.Quantile(1); reachable {
		t.Fatal("q=1 应落在没到的年份上")
	}
}

// TestQuantileMonotonic 分位随 q 不减；达到日相同时允许相等。
func TestQuantileMonotonic(t *testing.T) {
	mems := []Member{
		{Year: 1, Reached: true, Days: 20},
		{Year: 2, Reached: true, Days: 20},
		{Year: 3, Reached: true, Days: 35},
		{Year: 4, Reached: true, Days: 40},
	}
	d := NewDistribution(mems)
	prev := 0
	prevOK := true
	for i := 0; i <= 100; i++ {
		q := float64(i) / 100
		v, ok, err := d.Quantile(q)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			if !prevOK || v < prev {
				t.Fatalf("q=%g 日期倒退：prev=%d cur=%d", q, prev, v)
			}
			prev = v
		}
		prevOK = ok
	}
}

// TestAllUnreached 全部年份都没到时：范围为空，任何分位都到不了。
func TestAllUnreached(t *testing.T) {
	d := NewDistribution([]Member{{Year: 1, Reached: false}, {Year: 2, Reached: false}})
	if _, ok := d.Earliest(); ok {
		t.Fatal("无达到年，最早应为空")
	}
	if _, ok := d.Latest(); ok {
		t.Fatal("无达到年，最晚应为空")
	}
	if _, reachable, _ := d.Quantile(0.5); reachable {
		t.Fatal("任何分位都到不了")
	}
	if d.ReachedBy(10000) != 0 {
		t.Fatal("任何日期前达到数都应为 0")
	}
}

// TestProportionMonotonic 达到年数/比例随查询日期不减，且不超过 N。
func TestProportionMonotonic(t *testing.T) {
	d := NewDistribution([]Member{
		{Year: 1, Reached: true, Days: 10},
		{Year: 2, Reached: true, Days: 20},
		{Year: 3, Reached: true, Days: 20},
		{Year: 4, Reached: false},
	})
	prev := -1
	for day := 0; day <= 300; day++ {
		k := d.ReachedBy(day)
		if k < prev {
			t.Fatalf("day=%d 比例倒退：%d -> %d", day, prev, k)
		}
		if k > d.N() {
			t.Fatal("达到年数不应超过总年数")
		}
		prev = k
	}
	if d.ReachedBy(9) != 0 || d.ReachedBy(10) != 1 || d.ReachedBy(19) != 1 ||
		d.ReachedBy(20) != 3 || d.ReachedBy(math.MaxInt32) != 3 {
		t.Fatal("边界计数错误")
	}
}

func TestQuantileInvalid(t *testing.T) {
	d := NewDistribution([]Member{{Year: 1, Reached: true, Days: 1}})
	for _, q := range []float64{-0.01, 1.01, math.NaN()} {
		if _, _, err := d.Quantile(q); err == nil {
			t.Fatalf("q=%g 应报错", q)
		}
	}
	// 空分布不报错但不可达。
	var empty Distribution
	if v, ok, err := empty.Quantile(0.5); err != nil || ok || v != 0 {
		t.Fatalf("空分布：v=%d ok=%v err=%v", v, ok, err)
	}
}
