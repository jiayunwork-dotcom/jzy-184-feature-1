// Package ensemble 实现“把往年试走一遍”的年际推演与分位统计，是不依赖
// 数据库的纯计算部分：
//
//   - 日温度序列由上层（engine）按绑定站、历年记录与气候平均解析好后喂入；
//   - 本包负责在每个试走年份上累计积温、判定各阶段到达日（Arrival），
//     以及在年份样本上做最早/最晚、分位、“某天前到达比例”的统计。
//
// 分位口径（反经验 CDF 的次序统计量，与“到达比例”查询严格同口径）：
// 参与年份按到达日升序排列，窗口内未到的年份排在最后（视为 +∞）。
// q 分位取第 ceil(q*N) 个年份的到达日；该位置落在未到达年份上时，
// 如实返回“该分位在窗口内到不了”，绝不用最晚日或窗口末日顶替。
// 这样天然保证：q 不减则日期不减；样本中 k/N 的年份不晚于 q=k/N 分位。
package ensemble

import (
	"fmt"
	"sort"
	"time"

	"agristation/internal/gdd"
)

// Arrival 一个试走年份在某个阶段上的到达情况。
type Arrival struct {
	Year    int
	Reached bool
	// Date 为该年阶段到达日（本季绝对日期），Reached=false 时无意义。
	Date time.Time
}

// OrderedArrivals 按“到达日升序、未到年最后、同年同日按年份”排序，
// 返回排序后的副本。
func OrderedArrivals(as []Arrival) []Arrival {
	out := append([]Arrival(nil), as...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Reached != b.Reached {
			return a.Reached // true 在前
		}
		if !a.Reached {
			return a.Year < b.Year
		}
		if !a.Date.Equal(b.Date) {
			return a.Date.Before(b.Date)
		}
		return a.Year < b.Year
	})
	return out
}

// QuantileIndex 返回 q 分位在长度为 n 的有序样本中的下标（0 起）：ceil(q*n)-1。
func QuantileIndex(n int, q float64) int {
	if n == 0 {
		return -1
	}
	idx := int(q*float64(n) + 1 - 1e-9) // ceil(q*n)
	if idx < 1 {
		idx = 1
	}
	if idx > n {
		idx = n
	}
	return idx - 1
}

// Quantile 在已排序样本上取 q 分位。样本在该位置上未到达时
// reachable=false——调用方应如实表达“窗口内到不了”。
func Quantile(ordered []Arrival, q float64) (date time.Time, reachable bool) {
	idx := QuantileIndex(len(ordered), q)
	if idx < 0 {
		return time.Time{}, false
	}
	a := ordered[idx]
	return a.Date, a.Reached
}

// Earliest 返回已到达年份中的最早日期；全部未到则 nil。
func Earliest(ordered []Arrival) *time.Time {
	for i := range ordered {
		if ordered[i].Reached {
			d := ordered[i].Date
			return &d
		}
	}
	return nil
}

// Latest 返回已到达年份中的最晚日期；全部未到则 nil。
func Latest(ordered []Arrival) *time.Time {
	for i := len(ordered) - 1; i >= 0; i-- {
		if ordered[i].Reached {
			d := ordered[i].Date
			return &d
		}
	}
	return nil
}

// NotReachedCount 统计窗口内未到达的年份数。
func NotReachedCount(ordered []Arrival) int {
	n := 0
	for i := range ordered {
		if !ordered[i].Reached {
			n++
		}
	}
	return n
}

// FractionBy 返回在 by 当天或之前到达的年份比例（未到年算分子 0）。
// 样本为空时返回 0；是否可给出比例由上层根据样本量另行判断。
func FractionBy(ordered []Arrival, by time.Time) float64 {
	if len(ordered) == 0 {
		return 0
	}
	k := 0
	for i := range ordered {
		if ordered[i].Reached && !ordered[i].Date.After(by) {
			k++
		}
	}
	return float64(k) / float64(len(ordered))
}

// FutureDay 是某个试走年份在未来外推区间一天的温度解析结果，由上层准备。
type FutureDay struct {
	// Date 为本季绝对日期（asOf 之后的那一天）。
	Date time.Time
	// Eligible=false 表示该年此日既无本站历年记录也无气候平均，
	// 该年整体不能参与试走（不能用 GDD=0 静默顶替）。
	Eligible bool
	// Observed 为 true 表示温度来自该年本站真实历史记录；
	// false 表示由气候平均顶缺。用于“每个绑定站段至少一个真实日”的判定。
	Observed bool
	TMax     float64
	TMin     float64
	// Zero 为 true 表示该日无生效绑定，按 GDD=0 处理且不参与段判定。
	Zero bool
	// Segment 为当日所属绑定站段标识（站+映射日历年+段序号），
	// 相同 Segment 的日子连续共享同一个“至少一个真实日”要求。
	Segment int
}

// YearInput 一个试走年份的完整未来日序列（已按日期升序）。
type YearInput struct {
	Year int
	Days []FutureDay
}

// Config 试走所需的品种与季内参数。
type Config struct {
	Method    gdd.Method
	BaseTemp  float64
	UpperTemp float64
	// PrefixCum 为截至 asOf 的累计积温（由当季观测/补值/外推序列读出，
	// 所有试走年份共享这一段“已经走过的路”）。
	PrefixCum float64
	// Thresholds 五阶段累计积温需求，按 model.StageOrder。
	Thresholds []float64
}

// YearEligible 判断该年未来日序列是否够格参与：
// 任何一天历史与气候平均都缺则失格；每个绑定站段至少有一个真实历史日，
// 否则该段完全由气候平均合成、不提供年际信息，该年也不参与。
func YearEligible(days []FutureDay) (bool, string) {
	segObserved := map[int]bool{}
	segExists := map[int]bool{}
	for i := range days {
		d := days[i]
		if d.Zero {
			continue
		}
		if !d.Eligible {
			return false, fmt.Sprintf("该年 %s 既无本站历年记录也无气候平均", d.Date.Format("01-02"))
		}
		segExists[d.Segment] = true
		if d.Observed {
			segObserved[d.Segment] = true
		}
	}
	for seg := range segExists {
		if !segObserved[seg] {
			return false, "存在完全由气候平均合成、没有任何真实历年记录的绑定站段"
		}
	}
	return true, ""
}

// SimulateYear 在一个够格年份的未来日序列上逐日累计，返回每个阈值的
// 到达情况（顺序与 Thresholds 相同）。PrefixCum 已达到的阈值不会出现在
// 这里——上层只对未达到的阶段做试走。
func SimulateYear(y YearInput, c Config) []Arrival {
	total := c.PrefixCum
	out := make([]Arrival, len(c.Thresholds))
	hit := make([]bool, len(c.Thresholds))
	for ti := range c.Thresholds {
		out[ti] = Arrival{Year: y.Year}
	}
	for i := range y.Days {
		d := y.Days[i]
		if !d.Zero {
			total += gdd.Daily(c.Method, d.TMax, d.TMin, c.BaseTemp, c.UpperTemp)
		}
		for ti := range c.Thresholds {
			if hit[ti] {
				continue
			}
			if total+1e-9 >= c.Thresholds[ti] {
				hit[ti] = true
				out[ti] = Arrival{Year: y.Year, Reached: true, Date: d.Date}
			}
		}
	}
	return out
}
