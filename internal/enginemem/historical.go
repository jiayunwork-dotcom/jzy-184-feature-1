package enginemem

import (
	"errors"
	"sort"

	"agristation/internal/model"
)

// ErrFailHistReplace 是测试注入的“导入写一半失败”错误。
var ErrFailHistReplace = errors.New("模拟：整年导入写入中途失败")

// SetFailHistoricalReplace 开关：true 时下一次 ReplaceHistoricalYear
// 在删除旧年后、写入新年前返回错误，驱动整笔事务回滚。
func (m *Store) SetFailHistoricalReplace(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failHistReplace = on
}

// ListNormals 返回某站全部 DOY 的气候平均（升序）。
func (t *memTx) ListNormals(station string) ([]model.ClimateNormal, error) {
	out := make([]model.ClimateNormal, 0)
	for _, n := range t.m.normals {
		if n.StationCode == station {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DOY < out[j].DOY })
	return out, nil
}

// ReplaceHistoricalYear 在当前事务状态上整年替换（事务提交/回滚由
// Store.Update 的快照恢复保证，语义与 PG 单事务 DELETE+INSERT 相同）。
func (t *memTx) ReplaceHistoricalYear(station string, year int, rows []model.HistoricalTemp) error {
	for k, h := range t.m.hist {
		if h.StationCode == station && h.Year == year {
			delete(t.m.hist, k)
		}
	}
	if t.m.failHistReplace {
		t.m.failHistReplace = false
		return ErrFailHistReplace
	}
	for _, r := range rows {
		t.m.hist[histKey{station, dateKey(r.Date)}] = r
	}
	return nil
}

func (t *memTx) ListHistoricalStationRows(station string) ([]model.HistoricalTemp, error) {
	out := make([]model.HistoricalTemp, 0)
	for _, h := range t.m.hist {
		if h.StationCode == station {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

func (t *memTx) ListHistoricalYears(station string) ([]int, error) {
	seen := map[int]struct{}{}
	for _, h := range t.m.hist {
		if h.StationCode == station {
			seen[h.Year] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for y := range seen {
		out = append(out, y)
	}
	sort.Ints(out)
	return out, nil
}

// HistRows 返回全库历年行（测试诊断/参考器用）。
func (m *Store) HistRows() []model.HistoricalTemp {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.HistoricalTemp, 0, len(m.hist))
	for _, h := range m.hist {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StationCode != out[j].StationCode {
			return out[i].StationCode < out[j].StationCode
		}
		return out[i].Date.Before(out[j].Date)
	})
	return out
}
