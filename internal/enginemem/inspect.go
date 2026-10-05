package enginemem

import (
	"time"

	"agristation/internal/model"
)

// ActiveStation 返回地块在某日生效绑定的站号（无绑定返回空串）。
// 主要供测试/诊断使用。
func (m *Store) ActiveStation(plotCode string, day time.Time) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	station := ""
	var latest time.Time
	for _, b := range m.bindings[plotCode] {
		if !b.EffectiveDate.After(day) {
			if station == "" || b.EffectiveDate.After(latest) {
				station = b.StationCode
				latest = b.EffectiveDate
			}
		}
	}
	return station
}

// TruncateDailyFrom 删除某地块自 from（含）起的快照行，模拟服务在重算
// 中途崩溃留下的残缺状态（测试重启续跑用）。
func (m *Store) TruncateDailyFrom(plotCode string, from time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.daily[plotCode][:0]
	for _, r := range m.daily[plotCode] {
		if r.Date.Before(from) {
			kept = append(kept, r)
		}
	}
	m.daily[plotCode] = kept
	m.stages[plotCode] = nil
}

// Tx 是内存事务句柄，供泛型 engine 实例化使用。
type Tx = *memTx

// HistoricalRows 返回某站某年全部历年记录（按日期升序），供测试/检查使用。
func (m *Store) HistoricalRows(station string, year int) []model.HistoricalWeather {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{m: m}
	out, _ := tx.ListHistoricalStationYear(station, year)
	return out
}

// HistoricalYears 返回某站存在资料的年份集合。
func (m *Store) HistoricalYears(station string) []int {
	m.mu.Lock()
	defer m.mu.Unlock()
	tx := &memTx{m: m}
	out, _ := tx.ListHistoricalYears(station)
	return out
}

// LatestAsOf 返回所有地块快照里最大的 as_of（engine.Tx 方法）。
func (t *memTx) LatestAsOf() (model.Date, error) {
	var latest time.Time
	found := false
	for _, rows := range t.m.daily {
		for _, r := range rows {
			if !found || r.AsOf.After(latest) {
				latest = r.AsOf
				found = true
			}
		}
	}
	if !found {
		return time.Time{}, nil
	}
	return latest, nil
}
