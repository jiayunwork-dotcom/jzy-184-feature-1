package model

import "time"

// Date 是业务里的“日”，统一用 UTC 零点的 time.Time 表示。
// 所有传入的 YYYY-MM-DD 都按 UTC 解析，避免时区错位。
type Date = time.Time

// DateFrom 把 UTC 日期组件拼成 Date。
func DateFrom(year int, month time.Month, day int) Date {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// Stage 为玉米生育阶段。
type Stage string

const (
	StageEmergence Stage = "emergence" // 出苗
	StageJointing  Stage = "jointing"  // 拔节
	StageTasseling Stage = "tasseling" // 抽雄
	StageSilking   Stage = "silking"   // 吐丝
	StageMaturity  Stage = "maturity"  // 成熟
)

// StageOrder 是阶段的固定先后顺序。
var StageOrder = []Stage{
	StageEmergence,
	StageJointing,
	StageTasseling,
	StageSilking,
	StageMaturity,
}

// StageName 返回阶段的中文名，供接口展示。
func StageName(s Stage) string {
	switch s {
	case StageEmergence:
		return "出苗"
	case StageJointing:
		return "拔节"
	case StageTasseling:
		return "抽雄"
	case StageSilking:
		return "吐丝"
	case StageMaturity:
		return "成熟"
	}
	return string(s)
}

// Variety 品种热量需求。
type Variety struct {
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	BaseTemp   float64   `json:"base_temp"`  // 基点温度 ℃
	UpperTemp  float64   `json:"upper_temp"` // 上限温度 ℃
	Thresholds []float64 `json:"thresholds"` // 各阶段累计积温需求（℃·d），按 StageOrder
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Threshold 返回某阶段的累计积温需求。
func (v *Variety) Threshold(s Stage) float64 {
	for i, st := range StageOrder {
		if st == s && i < len(v.Thresholds) {
			return v.Thresholds[i]
		}
	}
	return 0
}

// Station 自动气象站。海拔单位米，经纬度十进制度。
type Station struct {
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	Elevation float64   `json:"elevation"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Observation 站点每日一条观测（同一站日可多次上报，序号大的为准）。
type Observation struct {
	StationCode string    `json:"station_code"`
	Date        Date      `json:"date"`
	TMax        float64   `json:"tmax"`
	TMin        float64   `json:"tmin"`
	Seq         int       `json:"seq"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Plot 地块。当前绑定用 Bindings 里 effective_date 最大且不超过查询日的一条。
type Plot struct {
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	SowDate   Date      `json:"sow_date"`
	Variety   string    `json:"variety"`
	Method    string    `json:"method"` // 积温口径：sine（默认）/triangle/mean
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Binding 地块-站点绑定，自 EffectiveDate 起生效。
type Binding struct {
	PlotCode      string    `json:"plot_code"`
	StationCode   string    `json:"station_code"`
	EffectiveDate Date      `json:"effective_date"`
	CreatedAt     time.Time `json:"created_at"`
}

// ClimateNormal 同站历年同日气候平均。DOY 为年内日序 1..366。
type ClimateNormal struct {
	StationCode string  `json:"station_code"`
	DOY         int     `json:"doy"`
	TMax        float64 `json:"tmax"`
	TMin        float64 `json:"tmin"`
}

// Source 数据来源标记。
type Source string

const (
	// SourceObserved 真实上报且赢得该站日的观测。
	SourceObserved Source = "observed"
	// SourceFilled 观测缺测时的补值（邻站或气候平均）。
	SourceFilled Source = "filled"
	// SourceClimate 预测未来日所用的气候平均外推。
	SourceClimate Source = "climate"
	// SourceMissing 连补值都没有的缺测日，当日积温按 0 处理。
	SourceMissing Source = "missing"
)

// FillMethod 标记补值的具体来源。
type FillMethod string

const (
	FillNone     FillMethod = ""               // 未补值
	FillNeighbor FillMethod = "neighbor"       // 邻站同日观测 + 海拔修正
	FillNormal   FillMethod = "climate_normal" // 本站历年同日气候平均
)

// DailyValue 某地块某一天的逐日积温明细。
type DailyValue struct {
	PlotCode   string     `json:"plot_code"`
	Date       Date       `json:"date"`
	TMax       *float64   `json:"tmax"` // 原始/补值所用温度，未来外推也有值
	TMin       *float64   `json:"tmin"`
	GDD        float64    `json:"gdd"`
	Cumulative float64    `json:"cumulative"`
	Source     Source     `json:"source"`
	FillMethod FillMethod `json:"fill_method"`
	// FillFrom 邻站补值时的来源站号。
	FillFrom *string `json:"fill_from,omitempty"`
	// AsOf 该行是基于哪天之前的数据算出的；每次重算到最新日。
	AsOf Date `json:"as_of"`
}

// StageStatus 阶段状态：reached=已达到（给实际日期），forecast=预计。
type StageStatus string

const (
	StatusReached  StageStatus = "reached"
	StatusForecast StageStatus = "forecast"
)

// StageDate 阶段日期结果。
type StageDate struct {
	PlotCode  string      `json:"plot_code"`
	Stage     Stage       `json:"stage"`
	Threshold float64     `json:"threshold"`
	Status    StageStatus `json:"status"`
	// Date reached 为实际达到日；forecast 为预计日；无足够数据时为 nil。
	Date *Date `json:"date"`
	// CumulativeOnDate 该日累计积温（便于核对）。
	CumulativeOnDate *float64 `json:"cumulative_on_date"`
	AsOf             Date     `json:"as_of"`
}

// StageEvent 阶段日期变更事件。
type StageEvent struct {
	ID         int64       `json:"id"`
	EventID    string      `json:"event_id"` // 业务幂等键
	PlotCode   string      `json:"plot_code"`
	Stage      Stage       `json:"stage"`
	OldDate    *Date       `json:"old_date"`
	NewDate    *Date       `json:"new_date"`
	OldStatus  StageStatus `json:"old_status"`
	NewStatus  StageStatus `json:"new_status"`
	ChangeID   string      `json:"change_id"` // 触发变更的标识（一次数据变化一个）
	Reason     string      `json:"reason"`    // 触发原因描述，含站号与日期
	OccurredAt time.Time   `json:"occurred_at"`
}
