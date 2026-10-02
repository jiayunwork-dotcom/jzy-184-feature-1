package engine

import (
	"context"

	"agristation/internal/model"
)

// ListStations 返回全部站点（按站代码排序）。
func (s *Service[T]) ListStations(ctx context.Context) ([]model.Station, error) {
	var out []model.Station
	err := s.store.View(ctx, func(tx T) error {
		var err error
		out, err = tx.ListStations()
		return err
	})
	return out, err
}

// ListPlots 返回全部地块。
func (s *Service[T]) ListPlots(ctx context.Context) ([]model.Plot, error) {
	var out []model.Plot
	err := s.store.View(ctx, func(tx T) error {
		var err error
		out, err = tx.ListPlots()
		return err
	})
	return out, err
}

// ListBindings 返回某地块的改绑历史（按生效日升序）。
func (s *Service[T]) ListBindings(ctx context.Context, plot string) ([]model.Binding, error) {
	var out []model.Binding
	err := s.store.View(ctx, func(tx T) error {
		p, err := tx.GetPlot(plot)
		if err != nil {
			return err
		}
		if p == nil {
			return notFoundError{what: "地块", code: plot}
		}
		out, err = tx.ListBindings(plot)
		return err
	})
	return out, err
}

// GetVariety 查询品种。
func (s *Service[T]) GetVariety(ctx context.Context, code string) (*model.Variety, error) {
	var out *model.Variety
	err := s.store.View(ctx, func(tx T) error {
		var err error
		out, err = tx.GetVariety(code)
		return err
	})
	return out, err
}
