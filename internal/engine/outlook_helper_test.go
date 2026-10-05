package engine

import (
	"context"
	"math/rand"

	"agristation/internal/enginemem"
	"agristation/internal/model"
)

// histRowsForTest 经只读事务读某站全部历年行（测试辅助）。
func histRowsForTest(s *testSvc, station string) ([]model.HistoricalTemp, error) {
	var out []model.HistoricalTemp
	err := s.store.View(context.Background(), func(tx enginemem.Tx) error {
		var e error
		out, e = tx.ListHistoricalStationRows(station)
		return e
	})
	return out, err
}

// newSeeded 返回固定种子的随机源（测试辅助）。
func newSeeded(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed)) }
