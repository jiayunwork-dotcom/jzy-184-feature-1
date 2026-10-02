package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"agristation/internal/cum"
	"agristation/internal/model"
)

// RegisterStation 登记（或按代码覆盖）气象站。
func (s *Service[T]) RegisterStation(ctx context.Context, st model.Station) error {
	if st.Code == "" {
		return fmt.Errorf("站号不能为空")
	}
	if st.Latitude < -90 || st.Latitude > 90 {
		return fmt.Errorf("纬度超出 [-90,90]")
	}
	if st.Longitude < -180 || st.Longitude > 180 {
		return fmt.Errorf("经度超出 [-180,180]")
	}
	now := time.Now().UTC()
	return s.store.Update(ctx, func(tx T) error {
		old, err := tx.GetStation(st.Code)
		if err != nil {
			return err
		}
		if old != nil {
			st.CreatedAt = old.CreatedAt
		} else {
			st.CreatedAt = now
		}
		st.UpdatedAt = now
		if err := tx.PutStation(st); err != nil {
			return err
		}
		// 经纬度或海拔变化会改变邻站选择与海拔修正：全量重建所有地块。
		if old != nil && (old.Latitude != st.Latitude || old.Longitude != st.Longitude ||
			old.Elevation != st.Elevation) {
			plots, err := tx.ListPlots()
			if err != nil {
				return err
			}
			for _, p := range plots {
				plot := p
				if err := s.recomputePlot(tx, &plot, s.nowDate(), plot.SowDate,
					s.newID(), fmt.Sprintf("站点 %s 坐标或海拔变更", st.Code), true); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// RegisterVariety 登记或修改品种。
func (s *Service[T]) RegisterVariety(ctx context.Context, v model.Variety) error {
	if err := ValidateVariety(&v); err != nil {
		return err
	}
	now := time.Now().UTC()
	return s.store.Update(ctx, func(tx T) error {
		old, err := tx.GetVariety(v.Code)
		if err != nil {
			return err
		}
		if old != nil {
			v.CreatedAt = old.CreatedAt
		} else {
			v.CreatedAt = now
		}
		v.UpdatedAt = now
		if err := tx.PutVariety(v); err != nil {
			return err
		}
		// 品种参数变化影响使用它的所有地块的全部历史日：全量重建。
		plots, err := tx.ListPlots()
		if err != nil {
			return err
		}
		for _, p := range plots {
			if p.Variety != v.Code {
				continue
			}
			plot := p
			if err := s.recomputePlot(tx, &plot, s.nowDate(), plot.SowDate,
				s.newID(), fmt.Sprintf("品种 %s 参数修改", v.Code), true); err != nil {
				return err
			}
		}
		return nil
	})
}

// RegisterPlot 登记地块；若已存在则修改。initialStation 为登记时的初始
// 绑定站（首次登记必填，与地块写入同一事务；已存在地块修改时可空）。
// 修改播种日、品种或口径会使整条序列失效：全量重建。
func (s *Service[T]) RegisterPlot(ctx context.Context, p model.Plot, initialStation string) (*model.Plot, error) {
	if p.Code == "" {
		return nil, fmt.Errorf("地块代码不能为空")
	}
	if p.Variety == "" {
		return nil, fmt.Errorf("必须指定品种")
	}
	if p.SowDate.IsZero() {
		return nil, fmt.Errorf("必须指定播种日期")
	}
	if err := ValidateMethod(p.Method); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	err := s.store.Update(ctx, func(tx T) error {
		v, err := tx.GetVariety(p.Variety)
		if err != nil {
			return err
		}
		if v == nil {
			return fmt.Errorf("品种 %s 不存在", p.Variety)
		}
		old, err := tx.GetPlot(p.Code)
		if err != nil {
			return err
		}
		if old == nil && initialStation == "" {
			return fmt.Errorf("登记地块必须指定初始绑定站点")
		}
		if initialStation != "" {
			st, err := tx.GetStation(initialStation)
			if err != nil {
				return err
			}
			if st == nil {
				return fmt.Errorf("绑定到不存在的站：%s", initialStation)
			}
		}
		structural := old == nil || !old.SowDate.Equal(p.SowDate) ||
			old.Method != p.Method || old.Variety != p.Variety
		if old != nil {
			p.CreatedAt = old.CreatedAt
		} else {
			p.CreatedAt = now
		}
		p.UpdatedAt = now
		if err := tx.PutPlot(p); err != nil {
			return err
		}
		if initialStation != "" {
			if err := tx.AddBinding(model.Binding{
				PlotCode:      p.Code,
				StationCode:   initialStation,
				EffectiveDate: p.SowDate,
				CreatedAt:     now,
			}); err != nil {
				return err
			}
		}
		plot := p
		return s.recomputePlot(tx, &plot, s.nowDate(), plot.SowDate,
			s.newID(), reasonForPlotChange(old, &p), structural)
	})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func reasonForPlotChange(old, neu *model.Plot) string {
	if old == nil {
		return fmt.Sprintf("地块 %s 登记", neu.Code)
	}
	if !old.SowDate.Equal(neu.SowDate) {
		return fmt.Sprintf("地块 %s 播种日期由 %s 改为 %s",
			neu.Code, old.SowDate.Format("2006-01-02"), neu.SowDate.Format("2006-01-02"))
	}
	return fmt.Sprintf("地块 %s 资料修改", neu.Code)
}

// BindPlotInput 改绑入参。
type BindPlotInput struct {
	PlotCode      string `json:"plot_code"`
	StationCode   string `json:"station_code"`
	EffectiveDate string `json:"effective_date"` // YYYY-MM-DD，自该日起生效
}

// BindPlot 把地块从某日起改绑到另一个站。改绑日当天及之后的序列可能变化，
// 但为稳妥（补值可能跨站引用），改绑按结构性变更全量重建该地块。
func (s *Service[T]) BindPlot(ctx context.Context, in BindPlotInput) error {
	d, err := time.Parse("2006-01-02", in.EffectiveDate)
	if err != nil {
		return fmt.Errorf("生效日期格式应为 YYYY-MM-DD：%q", in.EffectiveDate)
	}
	d = d.UTC()
	return s.store.Update(ctx, func(tx T) error {
		p, err := tx.GetPlot(in.PlotCode)
		if err != nil {
			return err
		}
		if p == nil {
			return fmt.Errorf("地块 %s 不存在", in.PlotCode)
		}
		st, err := tx.GetStation(in.StationCode)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("改绑到不存在的站：%s", in.StationCode)
		}
		if d.Before(p.SowDate) {
			return fmt.Errorf("改绑生效日 %s 早于播种日 %s",
				d.Format("2006-01-02"), p.SowDate.Format("2006-01-02"))
		}
		b := model.Binding{
			PlotCode:      in.PlotCode,
			StationCode:   in.StationCode,
			EffectiveDate: d,
			CreatedAt:     time.Now().UTC(),
		}
		if err := tx.AddBinding(b); err != nil {
			return err
		}
		plot := *p
		return s.recomputePlot(tx, &plot, s.nowDate(), plot.SowDate,
			s.newID(), fmt.Sprintf("地块 %s 自 %s 起改绑到站 %s",
				in.PlotCode, d.Format("2006-01-02"), in.StationCode), true)
	})
}

// PutClimateNormal 写入/更新站点历年同日气候平均（DOY 1..366）。
func (s *Service[T]) PutClimateNormal(ctx context.Context, c model.ClimateNormal) error {
	if c.StationCode == "" {
		return fmt.Errorf("站号不能为空")
	}
	if c.DOY < 1 || c.DOY > 366 {
		return fmt.Errorf("日序必须在 1..366")
	}
	if err := ValidateTemp(c.TMax, c.TMin); err != nil {
		return err
	}
	return s.store.Update(ctx, func(tx T) error {
		st, err := tx.GetStation(c.StationCode)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("站点 %s 不存在", c.StationCode)
		}
		if err := tx.PutClimateNormal(c); err != nil {
			return err
		}
		// 气候平均既用于缺测补值，也用于未来外推：任一变化都全量对齐所有地块。
		plots, err := tx.ListPlots()
		if err != nil {
			return err
		}
		for _, p := range plots {
			plot := p
			if err := s.recomputePlot(tx, &plot, s.nowDate(), plot.SowDate,
				s.newID(), fmt.Sprintf("站点 %s 历年同日气候平均更新", c.StationCode), true); err != nil {
				return err
			}
		}
		return nil
	})
}

// PlotDaily 查询地块逐日积温曲线。queryDate 不得早于播种日。
// from/to 为可选的日期区间（YYYY-MM-DD，空表示播种日..asOf+预测窗口）。
func (s *Service[T]) PlotDaily(ctx context.Context, plotCode, from, to string, queryDate string) (
	[]model.DailyValue, error) {
	var rows []model.DailyValue
	err := s.store.View(ctx, func(tx T) error {
		p, err := tx.GetPlot(plotCode)
		if err != nil {
			return err
		}
		if p == nil {
			return notFoundError{what: "地块", code: plotCode}
		}
		q := s.nowDate()
		if queryDate != "" {
			qd, err := time.Parse("2006-01-02", queryDate)
			if err != nil {
				return fmt.Errorf("查询日期格式应为 YYYY-MM-DD：%q", queryDate)
			}
			q = qd.UTC()
		}
		if err := ValidateSowQuery(p.SowDate, q); err != nil {
			return err
		}
		all, err := tx.ListDaily(plotCode)
		if err != nil {
			return err
		}
		var f, t *time.Time
		if from != "" {
			fd, err := time.Parse("2006-01-02", from)
			if err != nil {
				return fmt.Errorf("起始日期格式应为 YYYY-MM-DD：%q", from)
			}
			ft := fd.UTC()
			f = &ft
		}
		if to != "" {
			td, err := time.Parse("2006-01-02", to)
			if err != nil {
				return fmt.Errorf("结束日期格式应为 YYYY-MM-DD：%q", to)
			}
			tt := td.UTC()
			t = &tt
		}
		for _, r := range all {
			if f != nil && r.Date.Before(*f) {
				continue
			}
			if t != nil && r.Date.After(*t) {
				continue
			}
			rows = append(rows, r)
		}
		return nil
	})
	return rows, err
}

// PlotStages 查询地块阶段日期。
func (s *Service[T]) PlotStages(ctx context.Context, plotCode, queryDate string) ([]model.StageDate, error) {
	var out []model.StageDate
	err := s.store.View(ctx, func(tx T) error {
		p, err := tx.GetPlot(plotCode)
		if err != nil {
			return err
		}
		if p == nil {
			return notFoundError{what: "地块", code: plotCode}
		}
		q := s.nowDate()
		if queryDate != "" {
			qd, err := time.Parse("2006-01-02", queryDate)
			if err != nil {
				return fmt.Errorf("查询日期格式应为 YYYY-MM-DD：%q", queryDate)
			}
			q = qd.UTC()
		}
		if err := ValidateSowQuery(p.SowDate, q); err != nil {
			return err
		}
		all, err := tx.ListDaily(plotCode)
		if err != nil {
			return err
		}
		v, err := tx.GetVariety(p.Variety)
		if err != nil {
			return err
		}
		acc := make([]cum.Accumulated, 0, len(all))
		for _, r := range all {
			acc = append(acc, cum.Accumulated{
				Day: cum.Day{
					Date:       r.Date,
					TMax:       r.TMax,
					TMin:       r.TMin,
					GDD:        r.GDD,
					Source:     r.Source,
					FillMethod: r.FillMethod,
					FillFrom:   r.FillFrom,
				},
				Cumulative: r.Cumulative,
			})
		}
		for _, sr := range cum.Stages(acc, v, q) {
			out = append(out, model.StageDate{
				PlotCode:         plotCode,
				Stage:            sr.Stage,
				Threshold:        sr.Threshold,
				Status:           sr.Status,
				Date:             sr.Date,
				CumulativeOnDate: sr.Cum,
				AsOf:             q,
			})
		}
		return nil
	})
	return out, err
}

// ListEvents 按时间（含游标）拉取变更事件，供小程序轮询推送。
// afterID 为已收到的最大事件 ID；limit 默认 100、最大 500。
func (s *Service[T]) ListEvents(ctx context.Context, afterID int64, limit int, plot string) ([]model.StageEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	var out []model.StageEvent
	err := s.store.View(ctx, func(tx T) error {
		var err error
		out, err = tx.ListEventsAfter(afterID, limit, plot)
		return err
	})
	return out, err
}

// Recover 重启后续跑：以最终数据对所有地块全量重建，使结果与
// “只拿最终数据从播种日从头算一遍”逐日一致，同时把 asOf 推进到今天。
//
// 写入本身与重算在同一个数据库事务内提交，崩溃时整笔回滚、客户端可
// 整体重发；这里再做一次全量对齐，兜住任何中断并让预测随日期自然推进。
func (s *Service[T]) Recover(ctx context.Context) error {
	asOf := s.nowDate()
	plots := []model.Plot{}
	if err := s.store.View(ctx, func(tx T) error {
		var err error
		plots, err = tx.ListPlots()
		return err
	}); err != nil {
		return err
	}
	sort.Slice(plots, func(i, j int) bool { return plots[i].Code < plots[j].Code })
	for _, p := range plots {
		plot := p
		err := s.store.Update(ctx, func(tx T) error {
			return s.recomputePlot(tx, &plot, asOf, plot.SowDate, s.newID(),
				"服务重启后全量对齐", true)
		})
		if err != nil {
			return fmt.Errorf("地块 %s 重启重算失败：%w", plot.Code, err)
		}
	}
	return nil
}

// Rollover 在服务长期运行、日期自然跨天时推进预测基准：当快照的 as_of
// 早于今天时，对所有地块全量重建一次，使原“未来外推”日按最新观测/补值
// 重新判定。as_of 已是今天时直接返回，不做任何写入（也不产生事件）。
// 建议由调用方按小时级定时器触发。
func (s *Service[T]) Rollover(ctx context.Context) error {
	today := s.nowDate()
	roll := false
	if err := s.store.View(ctx, func(tx T) error {
		latest, err := tx.LatestAsOf()
		if err != nil {
			return err
		}
		roll = latest.Before(today)
		return nil
	}); err != nil {
		return err
	}
	if !roll {
		return nil
	}
	return s.Recover(ctx)
}
