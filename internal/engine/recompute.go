package engine

import (
	"fmt"
	"time"

	"agristation/internal/cum"
	"agristation/internal/fill"
	"agristation/internal/gdd"
	"agristation/internal/model"
)

// activeBindingAt 返回某日生效的绑定：effective_date 最大且不晚于 d 的一条。
func activeBindingAt(bindings []model.Binding, d model.Date) *model.Binding {
	var active *model.Binding
	for i := range bindings {
		b := &bindings[i]
		if !b.EffectiveDate.After(d) {
			if active == nil || b.EffectiveDate.After(active.EffectiveDate) {
				active = b
			}
		}
	}
	return active
}

// buildDay 构造地块在日期 d 的温度来源与日积温。
//
// 过去日（d <= asOf）：先取绑定站真实观测；缺测则邻站观测（海拔修正），
// 再回退本站气候平均；都没有标记 missing。
// 未来日（d > asOf）：直接用绑定站历年同日气候平均外推，标记 climate。
func (s *Service[T]) buildDay(tx T, plot *model.Plot, v *model.Variety, method gdd.Method,
	bindings []model.Binding, stations map[string]*model.Station, d, asOf model.Date) cum.Day {

	day := cum.Day{Date: d}
	b := activeBindingAt(bindings, d)
	if b == nil {
		day.Source = model.SourceMissing
		return day
	}
	st, ok := stations[b.StationCode]
	if !ok {
		day.Source = model.SourceMissing
		return day
	}

	compute := func(tmax, tmin float64) {
		day.TMax = &tmax
		day.TMin = &tmin
		day.GDD = gdd.Daily(method, tmax, tmin, v.BaseTemp, v.UpperTemp)
	}

	if !d.After(asOf) {
		obs, err := tx.GetObservation(st.Code, d)
		if err != nil {
			day.Source = model.SourceMissing
			return day
		}
		if obs != nil {
			day.Source = model.SourceObserved
			compute(obs.TMax, obs.TMin)
			return day
		}
		// 缺测：邻站同日真实观测。
		obsList, err := tx.ListObservationsOnDate(d)
		if err == nil {
			cands := make([]fill.Candidate, 0, len(obsList))
			for _, o := range obsList {
				if cs := stations[o.StationCode]; cs != nil {
					cands = append(cands, fill.Candidate{Station: *cs, TMax: o.TMax, TMin: o.TMin})
				}
			}
			if r := fill.Missing(*st, cands, s.getNormal(tx, st.Code, d)); r != nil {
				compute(r.TMax, r.TMin)
				day.Source = model.SourceFilled
				day.FillMethod = r.Method
				day.FillFrom = r.FromStation
				return day
			}
		}
		day.Source = model.SourceMissing
		return day
	}

	// 未来日：气候平均外推。
	if n := s.getNormal(tx, st.Code, d); n != nil {
		day.Source = model.SourceClimate
		compute(n.TMax, n.TMin)
		return day
	}
	day.Source = model.SourceMissing
	return day
}

// getNormal 取站点某日的历年同日气候平均，处理闰年 2/29。
func (s *Service[T]) getNormal(tx T, station string, d model.Date) *model.ClimateNormal {
	n, err := tx.GetClimate(station, fill.NormalDOY(d))
	if err != nil || n != nil {
		return n
	}
	if d.Month() == time.February && d.Day() == 29 {
		n, _ = tx.GetClimate(station, model.DateFrom(d.Year(), time.March, 1).YearDay())
	}
	return n
}

// recomputePlot 对单个地块执行重算并写入快照、阶段结果与事件。
//
// from 为受影响最早日期（可等于播种日）。from 之前的快照与累计值保持不动；
// 当 from==sowDate 时全量重建。fullSeries=true 时忽略种子累计（用于播种日/
// 品种/改绑这类结构性变更），效果与 from=sowDate 相同。
func (s *Service[T]) recomputePlot(tx T, plot *model.Plot, asOf model.Date,
	from model.Date, changeID, reason string, fullSeries bool) error {

	v, err := tx.GetVariety(plot.Variety)
	if err != nil {
		return err
	}
	if v == nil {
		return fmt.Errorf("地块 %s 引用了不存在的品种 %s", plot.Code, plot.Variety)
	}
	method, err := gdd.ParseMethod(plot.Method)
	if err != nil {
		return err
	}
	bindings, err := tx.ListBindings(plot.Code)
	if err != nil {
		return err
	}
	// 邻站补值可能引用全县任意站点，候选必须包含全部站，不能只含绑定站。
	allStations, err := tx.ListStations()
	if err != nil {
		return err
	}
	stations := make(map[string]*model.Station, len(allStations))
	for i := range allStations {
		st := allStations[i]
		stations[st.Code] = &st
	}

	sow := plot.SowDate
	if fullSeries || from.Before(sow) {
		from = sow
	}
	horizon := asOf.AddDate(0, 0, ForecastDays)

	// 种子：from 前一日累计积温。
	seed := 0.0
	if !fullSeries && from.After(sow) {
		seed, err = tx.CumulativeBefore(plot.Code, from)
		if err != nil {
			return err
		}
	}

	days := make([]cum.Day, 0)
	for d := from; !d.After(horizon); d = d.AddDate(0, 0, 1) {
		days = append(days, s.buildDay(tx, plot, v, method, bindings, stations, d, asOf))
	}
	acc := cum.Accumulate(cum.Series{PlotCode: plot.Code, Variety: v, Method: method, Days: days})

	rows := make([]model.DailyValue, 0, len(acc))
	total := seed
	for _, a := range acc {
		total += a.GDD
		rows = append(rows, model.DailyValue{
			PlotCode:   plot.Code,
			Date:       a.Date,
			TMax:       a.TMax,
			TMin:       a.TMin,
			GDD:        a.GDD,
			Cumulative: total,
			Source:     a.Source,
			FillMethod: a.FillMethod,
			FillFrom:   a.FillFrom,
			AsOf:       asOf,
		})
	}
	if err := tx.ReplaceDailyFrom(plot.Code, from, rows); err != nil {
		return err
	}

	// 阶段日期：在“播种日..horizon”的完整累计序列上判定。
	full, err := tx.ListDaily(plot.Code)
	if err != nil {
		return err
	}
	accAll := make([]cum.Accumulated, 0, len(full))
	for _, r := range full {
		accAll = append(accAll, cum.Accumulated{
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
	stageResults := cum.Stages(accAll, v, asOf)
	stageRows := make([]model.StageDate, 0, len(stageResults))
	for _, sr := range stageResults {
		stageRows = append(stageRows, model.StageDate{
			PlotCode:         plot.Code,
			Stage:            sr.Stage,
			Threshold:        sr.Threshold,
			Status:           sr.Status,
			Date:             sr.Date,
			CumulativeOnDate: sr.Cum,
			AsOf:             asOf,
		})
	}
	old, err := tx.ReplaceStageDates(plot.Code, stageRows)
	if err != nil {
		return err
	}

	// 变更事件：与旧阶段行逐日对比。
	oldMap := map[model.Stage]model.StageDate{}
	for _, o := range old {
		oldMap[o.Stage] = o
	}
	for _, nr := range stageRows {
		or, existed := oldMap[nr.Stage]
		if !existed {
			// 首次生成阶段结果不算“日期发生变化”的推送事件（此时地块刚登记）。
			continue
		}
		if sameStage(or, nr) {
			continue
		}
		ev := model.StageEvent{
			EventID:    fmt.Sprintf("%s:%s:%s", plot.Code, nr.Stage, changeID),
			PlotCode:   plot.Code,
			Stage:      nr.Stage,
			OldDate:    or.Date,
			NewDate:    nr.Date,
			OldStatus:  or.Status,
			NewStatus:  nr.Status,
			ChangeID:   changeID,
			Reason:     reason,
			OccurredAt: time.Now().UTC(),
		}
		if _, err := tx.InsertEvent(ev); err != nil {
			return err
		}
	}
	return nil
}

// sameStage 判断两条阶段结果是否等价（日期与状态都不变）。
func sameStage(a, b model.StageDate) bool {
	if a.Status != b.Status {
		return false
	}
	return datesEqual(a.Date, b.Date)
}

func datesEqual(a, b *model.Date) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
