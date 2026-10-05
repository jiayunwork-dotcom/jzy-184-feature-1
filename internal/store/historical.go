package store

import (
	"time"

	"agristation/internal/model"

	"github.com/jackc/pgx/v5"
)

// ---------- 气候平均（批量） ----------

func (t *pgTx) ListNormals(station string) ([]model.ClimateNormal, error) {
	rows, err := t.tx.Query(t.ctx, `
		SELECT station_code,doy,tmax,tmin FROM climate_normals
		WHERE station_code=$1 ORDER BY doy`, station)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ClimateNormal
	for rows.Next() {
		var c model.ClimateNormal
		if err := rows.Scan(&c.StationCode, &c.DOY, &c.TMax, &c.TMin); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ---------- 历年气温资料 ----------

const histCols = `station_code, year, date, tmax, tmin`

func scanHist(row pgx.Row) (model.HistoricalTemp, error) {
	var h model.HistoricalTemp
	var d time.Time
	err := row.Scan(&h.StationCode, &h.Year, &d, &h.TMax, &h.TMin)
	if err != nil {
		return model.HistoricalTemp{}, err
	}
	h.Date = dateOnly(d)
	return h, nil
}

// ReplaceHistoricalYear 同一事务内整年替换：先删该站该年，再批量写入。
func (t *pgTx) ReplaceHistoricalYear(station string, year int, rows []model.HistoricalTemp) error {
	if _, err := t.tx.Exec(t.ctx, `
		DELETE FROM historical_temps WHERE station_code=$1 AND year=$2`,
		station, year); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(`
			INSERT INTO historical_temps(station_code,year,date,tmax,tmin)
			VALUES ($1,$2,$3,$4,$5)`,
			r.StationCode, r.Year, r.Date, r.TMax, r.TMin)
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

func (t *pgTx) ListHistoricalStationRows(station string) ([]model.HistoricalTemp, error) {
	rows, err := t.tx.Query(t.ctx, `
		SELECT `+histCols+` FROM historical_temps
		WHERE station_code=$1 ORDER BY date`, station)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.HistoricalTemp
	for rows.Next() {
		h, err := scanHist(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (t *pgTx) ListHistoricalYears(station string) ([]int, error) {
	rows, err := t.tx.Query(t.ctx, `
		SELECT DISTINCT year FROM historical_temps
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
