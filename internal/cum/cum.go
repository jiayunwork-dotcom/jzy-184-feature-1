// Package cum 把逐日积温序列累计起来，并推算各生育阶段的日期。
//
// 阶段判定用“累计积温首次达到阈值的日期”。阈值按品种给出的各阶段
// 累计需求（不是阶段间增量）。出苗阶段同样按从播种日起累计达到出苗
// 阈值的日期判定。
//
// 状态区分：
//   - reached：截至 asOf（今天），累计积温已达到阈值，给实际达到日期；
//   - forecast：asOf 时尚未达到，但用未来气候平均外推能在预测窗口内达到，
//     给预计日期；窗口内仍达不到则日期为空（窗口长度由 engine 给）。
package cum

import (
	"agristation/internal/gdd"
	"agristation/internal/model"
)

// Day 是逐日序列上的一天。温度可为 nil（完全无数据、连补值也没有），
// 此时 GDD 按 0 处理但 Source 保留以区分。
type Day struct {
	Date       model.Date
	TMax       *float64
	TMin       *float64
	GDD        float64
	Source     model.Source
	FillMethod model.FillMethod
	FillFrom   *string
}

// Series 一条从播种日开始按日排列的积温序列。
type Series struct {
	PlotCode string
	Variety  *model.Variety
	Method   gdd.Method
	Days     []Day // 已按日期升序
}

// Accumulated 是累计后的一天。
type Accumulated struct {
	Day
	Cumulative float64
}

// Accumulate 逐日累计，返回带累计值的序列。
func Accumulate(s Series) []Accumulated {
	out := make([]Accumulated, 0, len(s.Days))
	var total float64
	for _, d := range s.Days {
		total += d.GDD
		out = append(out, Accumulated{Day: d, Cumulative: total})
	}
	return out
}

// StageResult 阶段推算结果。
type StageResult struct {
	Stage     model.Stage
	Threshold float64
	Status    model.StageStatus
	Date      *model.Date
	Cum       *float64
}

// Stages 在累计序列上推算全部阶段。
// asOf 之前（含）为已发生区间，之后为外推区间。
func Stages(acc []Accumulated, v *model.Variety, asOf model.Date) []StageResult {
	results := make([]StageResult, 0, len(model.StageOrder))
	for i, st := range model.StageOrder {
		thr := v.Thresholds[i]
		r := StageResult{Stage: st, Threshold: thr}
		for j := range acc {
			if acc[j].Cumulative+1e-9 < thr {
				continue
			}
			c := acc[j].Cumulative
			r.Cum = &c
			d := acc[j].Date
			r.Date = &d
			if !d.After(asOf) {
				r.Status = model.StatusReached
			} else {
				r.Status = model.StatusForecast
			}
			break
		}
		// 未达到阈值时 Status 为空、Date 为 nil。
		results = append(results, r)
	}
	return results
}
