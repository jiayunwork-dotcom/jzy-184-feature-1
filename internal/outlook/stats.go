// Package outlook 实现“把预测窗口剩下的日子拿各历史年份各试走一遍”后的
// 纯统计口径：在一组成员年份的阶段达到日（含“窗口内始终没到”）上取
// 最早/最晚、分位与“某天或之前达到的比例”。
//
// 试走（温度序列的构建、累计、阈值判定）在 engine 包完成；本包只处理
// 结果序列，不接触数据库，便于单独测试不变量：
//
//   - 分位随 q 增大日期不减；
//   - q 小到落在“没到”的尾部时如实报“窗口内到不了”，而不是补一个假日期；
//   - 比例随查询日期不减。
//
// 分位采用“按年数等权的离散分位”：n 个成员按达到日升序排，没达到的年
// 排在末尾（视为 +∞）。q 分位取第 r 个成员，r = max(1, ceil(q·n))
// （q=0 取第 1 个）。这样 0.1/0.5/0.9 对农户有直白解释：“每 10 个年份
// 里约有 1 个/一半/9 个不会比这天更晚”，也天然满足：当没达到的年份占
// 比达到 (1-q) 以上时，该分位如实落在“窗口内到不了”的成员上。
package outlook

import (
	"fmt"
	"math"
	"sort"
)

// Member 是一次试走的成员年份在单个阶段上的结果。
// Reached=false 表示该年在整个预测窗口内始终没达到阈值（Date 无意义）。
type Member struct {
	Year    int
	Reached bool
	// Days 该成员达到阶段的日期，用相对播种/asOf 的“序日偏移”表示，
	// 避免把具体历法年带进来（不同历史年份的同日偏移含义相同）。
	Days int
}

// Distribution 是某阶段全部成员年份的达到情况，按 Days 升序、未达到垫底。
type Distribution struct {
	members []Member
	reached int
}

// NewDistribution 构建并排序一个分布。入参会被拷贝后排序。
func NewDistribution(members []Member) Distribution {
	ms := append([]Member(nil), members...)
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].Reached != ms[j].Reached {
			return ms[i].Reached // 达到的排前面
		}
		if !ms[i].Reached {
			return ms[i].Year < ms[j].Year
		}
		return ms[i].Days < ms[j].Days
	})
	r := 0
	for _, m := range ms {
		if m.Reached {
			r++
		}
	}
	return Distribution{members: ms, reached: r}
}

// N 返回参与试走的总年数。
func (d Distribution) N() int { return len(d.members) }

// Unreached 返回窗口内始终没到的年数。
func (d Distribution) Unreached() int { return len(d.members) - d.reached }

// Earliest 与 Latest 返回达到了的年份中的最早/最晚序日偏移；
// 没有任何一年达到时 ok=false（调用方应给空日期，而不是编造）。
func (d Distribution) Earliest() (days int, ok bool) {
	if d.reached == 0 {
		return 0, false
	}
	return d.members[0].Days, true
}

func (d Distribution) Latest() (days int, ok bool) {
	if d.reached == 0 {
		return 0, false
	}
	return d.members[d.reached-1].Days, true
}

// Quantile 返回 q 分位。q 必须在 [0,1]：q=0 取最早一个，q=1 取最后一个。
// 若该分位落在“窗口内没到”的成员上，Reachable=false（到不了，不补日期）。
func (d Distribution) Quantile(q float64) (days int, reachable bool, err error) {
	if math.IsNaN(q) || q < 0 || q > 1 {
		return 0, false, fmt.Errorf("分位必须在 0 到 1 之间，收到 %g", q)
	}
	n := len(d.members)
	if n == 0 {
		return 0, false, nil
	}
	r := int(math.Ceil(q * float64(n)))
	if r < 1 {
		r = 1
	}
	if r > n {
		r = n
	}
	m := d.members[r-1]
	if !m.Reached {
		return 0, false, nil
	}
	return m.Days, true, nil
}

// ReachedBy 返回序日偏移不超过 byDays 的成员年数（未达到的不算）。
func (d Distribution) ReachedBy(byDays int) int {
	k := 0
	for _, m := range d.members {
		if m.Reached && m.Days <= byDays {
			k++
		}
	}
	return k
}
