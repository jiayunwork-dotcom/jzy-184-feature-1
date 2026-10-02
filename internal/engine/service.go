// Package engine 是业务核心：观测写入仲裁、缺测补值、累计积温与阶段
// 推算的增量重算、阶段变更事件。它不依赖 PostgreSQL，所有读写都通过
// Tx 接口完成；store 包给出 PostgreSQL 实现，测试用内存实现。
//
// 增量重算与快照方案（详见 docs/design.md）：
//
//   - plot_daily 每地块每天一行，含当日 GDD 与累计积温，本身就是逐日快照。
//   - 一次变化影响的最早日期为 from（观测/补值类变化恰好只影响当天：
//     当日 GDD 只取决于当日温度，邻站补值也只用同日观测；而更早日期的
//     快照及其累计值不变）。重算时保留 from 之前所有行，读取 from 前一天
//     的累计值作为种子，删除并重建 from..horizon 的行。
//   - 改绑、品种或播种日修改可能影响任意历史日，对该地块从播种日全量重建。
//   - 阶段日期在重算后的整条序列上重新判定，与旧阶段行逐日对比，发生变化
//     才写事件；事件带 change_id，(地块,阶段,change_id) 唯一，保证同一次
//     数据变化不产生重复事件。
package engine

import (
	"context"
	"time"

	"agristation/internal/model"
)

// ForecastDays 是未来气候平均外推的天数窗口。玉米全生育期通常不超过
// 150 天左右，270 天足以覆盖最晚成熟预测。
const ForecastDays = 270

// Tx 是一次事务内可用的数据访问接口。store 与内存测试各自实现。
type Tx interface {
	// 基础档案
	GetStation(code string) (*model.Station, error)
	ListStations() ([]model.Station, error)
	PutStation(s model.Station) error
	GetVariety(code string) (*model.Variety, error)
	PutVariety(v model.Variety) error
	GetPlot(code string) (*model.Plot, error)
	ListPlots() ([]model.Plot, error)
	PutPlot(p model.Plot) error
	ListBindings(plot string) ([]model.Binding, error)
	AddBinding(b model.Binding) error
	PutClimateNormal(c model.ClimateNormal) error

	// 观测
	// GetObservation 返回该站日当前“胜出”的观测。
	GetObservation(station string, d model.Date) (*model.Observation, error)
	ListObservationsOnDate(d model.Date) ([]model.Observation, error)
	PutObservation(o model.Observation) error // 仅在新记录胜出时调用
	// Stale 记录写入历史表（仅存档，不参与计算）。
	PutStaleObservation(o model.Observation) error

	// 气候平均
	GetClimate(station string, doy int) (*model.ClimateNormal, error)

	// 快照与结果
	CumulativeBefore(plot string, d model.Date) (float64, error) // d 之前最近一行的累计值；无则 0
	ReplaceDailyFrom(plot string, from model.Date, rows []model.DailyValue) error
	ListDaily(plot string) ([]model.DailyValue, error)
	// ReplaceStageDates 全量替换阶段行，返回被替换的旧行。
	ReplaceStageDates(plot string, rows []model.StageDate) ([]model.StageDate, error)
	InsertEvent(e model.StageEvent) (bool, error) // 幂等：已存在返回 (false,nil)
	// ListEventsAfter 按 ID 游标拉取变更事件（id 即按时间递增）。
	ListEventsAfter(afterID int64, limit int, plot string) ([]model.StageEvent, error)
	// LatestAsOf 返回任一地块快照中最大的 as_of；没有任何快照时返回零值。
	// 用于判断跨日滚动是否必要。
	LatestAsOf() (model.Date, error)
}

// Store 事务工厂。
type Store interface {
	Update(ctx context.Context, fn func(Tx) error) error
	View(ctx context.Context, fn func(Tx) error) error
}

// TxFactory 是泛型版事务工厂约束：允许具体实现用自己的、与 Tx 同构的
// 接口类型（如测试用的内存包），避免为了类型匹配而产生导入环。
type TxFactory[T Tx] interface {
	Update(ctx context.Context, fn func(T) error) error
	View(ctx context.Context, fn func(T) error) error
}

// Service 业务服务。T 为事务句柄类型，须满足 Tx。
type Service[T Tx] struct {
	store TxFactory[T]
	// nowFn 返回当前时间，生产环境用 time.Now，测试可注入固定时钟。
	nowFn func() time.Time
	newID func() string
}

// NewService 创建引擎服务。
func NewService[T Tx, F TxFactory[T]](f F) *Service[T] {
	return &Service[T]{
		store: f,
		nowFn: func() time.Time { return time.Now().UTC() },
		newID: uuidString,
	}
}

// SetClock 注入时钟（测试用，须在服务启动早期设置）。
func (s *Service[T]) SetClock(clock func() time.Time) {
	s.nowFn = clock
}

// nowDate 返回当前 UTC 日期。
func (s *Service[T]) nowDate() model.Date {
	t := s.nowFn()
	t = t.UTC()
	return model.DateFrom(t.Year(), t.Month(), t.Day())
}
