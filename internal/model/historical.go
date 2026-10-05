package model

// HistoricalTemp 是某站某历史年份一天的逐日最高/最低气温（历年资料）。
// 与当年观测（Observation）分开存放：历年资料只用于“把剩下的日子各试走
// 一遍”的集合推演，不参与单点预测、逐日曲线与阶段事件。
type HistoricalTemp struct {
	StationCode string  `json:"station_code"`
	Year        int     `json:"year"` // 冗余年份列，整体替换与按年索引用
	Date        Date    `json:"date"`
	TMax        float64 `json:"tmax"`
	TMin        float64 `json:"tmin"`
}

// Quantile 是一个分位上的试走结果。Reached=false（JSON 里 reachable=false）
// 表示该分位落在“窗口内始终没到”的年份上：这一分位在预测窗口内到不了，
// Date 为空，不能悄悄丢掉这些年份。
type Quantile struct {
	Q         float64 `json:"q"`
	Reachable bool    `json:"reachable"`
	Date      *Date   `json:"date,omitempty"`
}

// StageOutlook 单个阶段的历年试走范围。
//
//   - Status=reached：阶段在 asOf 前已实际达到，Earliest/Latest/各分位
//     全部收成 ActualDate（实际达到日），UnreachedYears=0；
//   - Status=forecast：NYears 为参与试走的年数，Earliest/Latest 只在
//     “窗口内达到了的年份”中取（可能为空），UnreachedYears 是窗口内
//     始终没达到阈值的年数。
type StageOutlook struct {
	Stage     Stage       `json:"stage"`
	Threshold float64     `json:"threshold"`
	Status    StageStatus `json:"status"`
	// ActualDate 已达到阶段的实际达到日；未达到为 nil。
	ActualDate *Date `json:"actual_date,omitempty"`
	// NYears 参与试走的历年数（asOf 次日生效绑定站、至少有一天资料的年份）。
	NYears int `json:"n_years"`
	// Earliest/Latest 窗口内达到了的年份中的最早/最晚日期；一年都没到则为 nil。
	Earliest *Date `json:"earliest,omitempty"`
	Latest   *Date `json:"latest,omitempty"`
	// UnreachedYears 在预测窗口内始终没达到该阶段的年数。
	UnreachedYears int `json:"unreached_years"`
	// Quantiles 各分位结果（按 q 升序）。
	Quantiles []Quantile `json:"quantiles"`
}

// Outlook 一块地五个阶段的试走范围。
type Outlook struct {
	PlotCode string         `json:"plot_code"`
	AsOf     Date           `json:"as_of"`
	Horizon  Date           `json:"horizon"` // 预测窗口末日（asOf+270）
	Stages   []StageOutlook `json:"stages"`
}

// StageProbability “给定阶段与某一天，有多大比例的历年在这天或之前达到”。
type StageProbability struct {
	PlotCode  string      `json:"plot_code"`
	Stage     Stage       `json:"stage"`
	Threshold float64     `json:"threshold"`
	Status    StageStatus `json:"status"`
	// ActualDate 已达到阶段的实际达到日；比例在它之前为 0、当天起为 1。
	ActualDate *Date `json:"actual_date,omitempty"`
	AsOf       Date  `json:"as_of"`
	By         Date  `json:"by"`
	NYears     int   `json:"n_years"`
	// ReachedBy 在 By 当天或之前达到的年数。
	ReachedBy  int     `json:"reached_by"`
	Proportion float64 `json:"proportion"`
}
