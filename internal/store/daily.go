package store

import (
	"time"

	"agristation/internal/model"

	"github.com/jackc/pgx/v5"
)

// LatestAsOf 返回快照中最大的 as_of；无快照返回零值时间。
func (t *pgTx) LatestAsOf() (model.Date, error) {
	var d time.Time
	err := t.tx.QueryRow(t.ctx, `SELECT max(as_of) FROM plot_daily`).Scan(&d)
	if err == pgx.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		// max(NULL 表) 返回 NULL，pgx Scan 到 time.Time 会报错，按无数据处理。
		return time.Time{}, nil
	}
	return dateOnly(d), nil
}

// CumulativeBefore 返回 d 之前最近一天的累计积温；没有则 0。
func (t *pgTx) CumulativeBefore(plot string, d model.Date) (float64, error) {
	var cum float64
	err := t.tx.QueryRow(t.ctx, `
		SELECT cumulative FROM plot_daily
		WHERE plot_code=$1 AND date < $2
		ORDER BY date DESC LIMIT 1`, plot, d).Scan(&cum)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return cum, nil
}

// ReplaceDailyFrom 删除 plot 自 from（含）起的快照行，写入新行。
// 单条 DELETE + 批量拷贝在同一事务内完成。
func (t *pgTx) ReplaceDailyFrom(plot string, from model.Date, rows []model.DailyValue) error {
	if _, err := t.tx.Exec(t.ctx,
		`DELETE FROM plot_daily WHERE plot_code=$1 AND date >= $2`, plot, from); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO plot_daily(plot_code,date,tmax,tmin,gdd,cumulative,
				source,fill_method,fill_from,as_of)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (plot_code,date) DO UPDATE SET
				tmax=EXCLUDED.tmax, tmin=EXCLUDED.tmin, gdd=EXCLUDED.gdd,
				cumulative=EXCLUDED.cumulative, source=EXCLUDED.source,
				fill_method=EXCLUDED.fill_method, fill_from=EXCLUDED.fill_from,
				as_of=EXCLUDED.as_of`,
			r.PlotCode, r.Date, r.TMax, r.TMin, r.GDD, r.Cumulative,
			string(r.Source), string(r.FillMethod), r.FillFrom, r.AsOf)
	}
	br := t.tx.SendBatch(t.ctx, batch)
	for range rows {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return err
		}
	}
	return br.Close()
}

func (t *pgTx) ListDaily(plot string) ([]model.DailyValue, error) {
	rows, err := t.tx.Query(t.ctx, `
		SELECT plot_code,date,tmax,tmin,gdd,cumulative,source,fill_method,fill_from,as_of
		FROM plot_daily WHERE plot_code=$1 ORDER BY date`, plot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DailyValue
	for rows.Next() {
		var r model.DailyValue
		var d, asOf time.Time
		var src, fm string
		if err := rows.Scan(&r.PlotCode, &d, &r.TMax, &r.TMin, &r.GDD, &r.Cumulative,
			&src, &fm, &r.FillFrom, &asOf); err != nil {
			return nil, err
		}
		r.Date = dateOnly(d)
		r.AsOf = dateOnly(asOf)
		r.Source = model.Source(src)
		r.FillMethod = model.FillMethod(fm)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReplaceStageDates 全量替换该地块阶段行，返回被替换的旧行（供事件对比）。
func (t *pgTx) ReplaceStageDates(plot string, rows []model.StageDate) ([]model.StageDate, error) {
	oldRows, err := t.listStages(plot)
	if err != nil {
		return nil, err
	}
	if _, err := t.tx.Exec(t.ctx,
		`DELETE FROM plot_stages WHERE plot_code=$1`, plot); err != nil {
		return nil, err
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO plot_stages(plot_code,stage,threshold,status,date,cumulative,as_of)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (plot_code,stage) DO UPDATE SET
				threshold=EXCLUDED.threshold, status=EXCLUDED.status,
				date=EXCLUDED.date, cumulative=EXCLUDED.cumulative, as_of=EXCLUDED.as_of`,
			r.PlotCode, string(r.Stage), r.Threshold, string(r.Status),
			r.Date, r.CumulativeOnDate, r.AsOf)
	}
	br := t.tx.SendBatch(t.ctx, batch)
	for range rows {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return nil, err
		}
	}
	if err := br.Close(); err != nil {
		return nil, err
	}
	return oldRows, nil
}

func (t *pgTx) listStages(plot string) ([]model.StageDate, error) {
	rows, err := t.tx.Query(t.ctx,
		`SELECT plot_code,stage,threshold,status,date,cumulative,as_of
		 FROM plot_stages WHERE plot_code=$1`, plot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.StageDate
	for rows.Next() {
		var r model.StageDate
		var st, status string
		var d *time.Time
		if err := rows.Scan(&r.PlotCode, &st, &r.Threshold, &status, &d, &r.CumulativeOnDate, &r.AsOf); err != nil {
			return nil, err
		}
		r.Stage = model.Stage(st)
		r.Status = model.StageStatus(status)
		if d != nil {
			dd := dateOnly(*d)
			r.Date = &dd
		}
		r.AsOf = dateOnly(r.AsOf)
		out = append(out, r)
	}
	return out, rows.Err()
}

// InsertEvent 幂等插入：event_id 已存在时返回 false，不报错。
func (t *pgTx) InsertEvent(e model.StageEvent) (bool, error) {
	ct, err := t.tx.Exec(t.ctx, `
		INSERT INTO stage_events(event_id,plot_code,stage,old_date,new_date,
			old_status,new_status,change_id,reason,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (event_id) DO NOTHING`,
		e.EventID, e.PlotCode, string(e.Stage), e.OldDate, e.NewDate,
		string(e.OldStatus), string(e.NewStatus), e.ChangeID, e.Reason, e.OccurredAt)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

// ListEventsAfter 按 ID 游标拉事件（id 即按时间递增）。
func (t *pgTx) ListEventsAfter(afterID int64, limit int, plot string) ([]model.StageEvent, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if plot != "" {
		rows, err = t.tx.Query(t.ctx, `
			SELECT id,event_id,plot_code,stage,old_date,new_date,
				old_status,new_status,change_id,reason,occurred_at
			FROM stage_events
			WHERE id > $1 AND plot_code = $3
			ORDER BY id LIMIT $2`, afterID, limit, plot)
	} else {
		rows, err = t.tx.Query(t.ctx, `
			SELECT id,event_id,plot_code,stage,old_date,new_date,
				old_status,new_status,change_id,reason,occurred_at
			FROM stage_events
			WHERE id > $1
			ORDER BY id LIMIT $2`, afterID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.StageEvent
	for rows.Next() {
		var e model.StageEvent
		var stage string
		var oldD, newD *time.Time
		if err := rows.Scan(&e.ID, &e.EventID, &e.PlotCode, &stage, &oldD, &newD,
			&e.OldStatus, &e.NewStatus, &e.ChangeID, &e.Reason, &e.OccurredAt); err != nil {
			return nil, err
		}
		e.Stage = model.Stage(stage)
		if oldD != nil {
			d := dateOnly(*oldD)
			e.OldDate = &d
		}
		if newD != nil {
			d := dateOnly(*newD)
			e.NewDate = &d
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
