// Package fill 负责缺测日补值。
//
// 默认策略（也是实现的唯一策略，方法名 neighbor_then_normal）：
//
//  1. 优先用邻站同日真实观测，按海拔直减率修正后使用。
//     选站：与缺测站大圆距离最近、海拔差不超过 MaxElevDiff、
//     且当天有观测（且上报已“赢”得该站日）的站；取最近的一个。
//     温度修正按环境直减率 LapseRate（默认 0.0065 ℃/m）：
//     T_target = T_donor + lapse * (elev_donor - elev_target)
//     （捐赠站海拔更高时降温，反之升温）。
//  2. 邻站也没有时，回退到缺测站“历年同日（DOY）”的气候平均；
//     闰年 2/29 无值时回退到 3/1。
//  3. 两者都没有则不补：该日保持缺测，GDD 记 0 并标记 missing，
//     不编造温度。
//
// 为什么邻站优先：县级几十个站间距通常几公里到几十公里，同一天的天气
// 过程高度同步；同日观测能捕捉当日冷暖异常，而气候平均只是“平常那天”。
// 海拔修正可避免把山上站点的系统性偏差引入河谷地块。邻站全缺（例如
// 区域性断网）时再退到气候平均，保证总能给出可外推的序列。
//
// 补值只用于逐日积温的临时计算与展示，真实数据到达后由 engine 重算，
// 自动替换补值（见 docs/design.md）。
package fill

import (
	"math"
	"time"

	"agristation/internal/model"
)

const (
	// LapseRate 环境直减率 ℃/m。
	LapseRate = 0.0065
	// MaxElevDiff 海拔差超过此值的站不作为捐赠站（修正外推不可靠）。
	MaxElevDiff = 800.0
	// MaxDistance 距离超过此值（米）不作为捐赠站。
	MaxDistance = 80000.0
	earthRadius = 6371000.0
)

// Candidate 是一个可作为捐赠的邻站当日观测。
type Candidate struct {
	Station model.Station
	TMax    float64
	TMin    float64
}

// Result 是补值结果。
type Result struct {
	TMax        float64
	TMin        float64
	Method      model.FillMethod
	FromStation *string
}

// Haversine 大圆距离（米）。
func Haversine(lat1, lon1, lat2, lon2 float64) float64 {
	la1 := lat1 * math.Pi / 180
	la2 := lat2 * math.Pi / 180
	dla := (lat2 - lat1) * math.Pi / 180
	dlo := (lon2 - lon1) * math.Pi / 180
	h := math.Sin(dla/2)*math.Sin(dla/2) +
		math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * earthRadius * math.Asin(math.Sqrt(h))
}

// NormalDOY 返回某日期应查的气候平均日序；2/29 缺值时调用方回退到 DOY 60。
func NormalDOY(d model.Date) int {
	return d.YearDay()
}

// Missing 用邻站观测与本站气候平均补某缺测日。
// target 为缺测站；candidates 为当天有真实观测的其他站；
// normal 为本站该日序气候平均（可为 nil）。返回 nil 表示无可用补值。
func Missing(target model.Station, candidates []Candidate, normal *model.ClimateNormal) *Result {
	best := -1
	bestDist := math.MaxFloat64
	for i, c := range candidates {
		if c.Station.Code == target.Code {
			continue
		}
		elevDiff := math.Abs(c.Station.Elevation - target.Elevation)
		if elevDiff > MaxElevDiff {
			continue
		}
		d := Haversine(target.Latitude, target.Longitude, c.Station.Latitude, c.Station.Longitude)
		if d > MaxDistance {
			continue
		}
		if d < bestDist {
			bestDist = d
			best = i
		}
	}
	if best >= 0 {
		c := candidates[best]
		// 捐赠站海拔更高则目标处更暖（温度随海拔递减）：
		// T_target = T_donor + lapse * (elev_donor - elev_target)。
		adj := LapseRate * (c.Station.Elevation - target.Elevation)
		from := c.Station.Code
		return &Result{
			TMax:        c.TMax + adj,
			TMin:        c.TMin + adj,
			Method:      model.FillNeighbor,
			FromStation: &from,
		}
	}
	if normal != nil {
		return &Result{
			TMax:   normal.TMax,
			TMin:   normal.TMin,
			Method: model.FillNormal,
		}
	}
	return nil
}

// Adjust 气候平均回退时处理闰年：调用方负责在 DOY=366 查不到时改查 3 月 1 日。
func AdjustLeap(d model.Date) time.Time {
	if d.Month() == time.February && d.Day() == 29 {
		return time.Date(d.Year(), time.March, 1, 0, 0, 0, 0, time.UTC)
	}
	return d
}
