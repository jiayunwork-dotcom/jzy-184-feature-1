package store

import (
	"time"

	"agristation/internal/model"

	"github.com/jackc/pgx/v5"
)

const histCols = `station_code, year, date, tmax, tmin, imported_at`

// importedAtScan 丢弃 imported_at（业务不读它，仅作溯源）。
type importedAtScan struct{}

func (importedAtScan) Scan(src any) error { return nil }

// ListHistoricalYears 返回某站存在资料的年份（升序去重）。
func (t *pgTx) ListHistoricalYears(station string) ([]int, error) {
	rows, err := t.tx.Query(t.ctx, `
		SELECT DISTINCT year FROM historical_weather
		WHERE station_code=$1 ORDER BY year`, station)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var y int
		if err := rows.Scan(&y); err != nil {
			return nil, err
		}
		out = append(out, y)
	}
	return out, rows.Err()
}

// ListHistoricalStationYear 返回某站某年全部记录（按日期升序）。
func (t *pgTx) ListHistoricalStationYear(station string, year int) ([]model.HistoricalWeather, error) {
	rows, err := t.tx.Query(t.ctx, `
		SELECT `+histCols+` FROM historical_weather
		WHERE station_code=$1 AND year=$2 ORDER BY date`, station, year)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectHistorical(rows)
}

// ListHistorical 返回给定站集合中、日期在 [from,to] 内的历年记录（按日期升序）。
func (t *pgTx) ListHistorical(stations []string, from, to model.Date) ([]model.HistoricalWeather, error) {
	if len(stations) == 0 {
		return nil, nil
	}
	rows, err := t.tx.Query(t.ctx, `
		SELECT `+histCols+` FROM historical_weather
		WHERE station_code = ANY($1) AND date BETWEEN $2 AND $3
		ORDER BY date`, stations, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectHistorical(rows)
}

func collectHistorical(rows pgx.Rows) ([]model.HistoricalWeather, error) {
	var out []model.HistoricalWeather
	for rows.Next() {
		var station string
		var year int
		var d time.Time
		var tmax, tmin float64
		if err := rows.Scan(&station, &year, &d, &tmax, &tmin, &importedAtScan{}); err != nil {
			return nil, err
		}
		out = append(out, model.HistoricalWeather{
			StationCode: station,
			Year:        year,
			Date:        dateOnly(d),
			TMax:        tmax,
			TMin:        tmin,
		})
	}
	return out, rows.Err()
}

// ReplaceHistoricalStationYear 在当前事务内整年替换：先整段删除再批量插入。
// 调用方保证 rows 均属于同一站同一年。与事务提交绑定：事务回滚后旧年
// 内容原样保留，任何读事务都不会看到“删了旧年、新年只写了一半”的状态。
func (t *pgTx) ReplaceHistoricalStationYear(station string, year int, rows []model.HistoricalWeather) error {
	if _, err := t.tx.Exec(t.ctx,
		`DELETE FROM historical_weather WHERE station_code=$1 AND year=$2`,
		station, year); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO historical_weather(station_code,year,date,tmax,tmin,imported_at)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			r.StationCode, r.Year, r.Date, r.TMax, r.TMin, time.Now().UTC())
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
