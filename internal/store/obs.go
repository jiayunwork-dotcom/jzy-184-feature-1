package store

import (
	"time"

	"agristation/internal/model"

	"github.com/jackc/pgx/v5"
)

// ---------- 观测 ----------

const obsCols = `station_code, date, tmax, tmin, seq, updated_at`

func scanObs(row pgx.Row) (*model.Observation, error) {
	var o model.Observation
	var d time.Time
	err := row.Scan(&o.StationCode, &d, &o.TMax, &o.TMin, &o.Seq, &o.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.Date = dateOnly(d)
	return &o, nil
}

func (t *pgTx) GetObservation(station string, d model.Date) (*model.Observation, error) {
	return scanObs(t.tx.QueryRow(t.ctx, `
		SELECT `+obsCols+` FROM observations
		WHERE station_code=$1 AND date=$2`, station, d))
}

func (t *pgTx) ListObservationsOnDate(d model.Date) ([]model.Observation, error) {
	rows, err := t.tx.Query(t.ctx, `
		SELECT `+obsCols+` FROM observations WHERE date=$1 ORDER BY station_code`, d)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Observation
	for rows.Next() {
		o, err := scanObs(rows)
		if err != nil {
			return nil, err
		}
		if o != nil {
			out = append(out, *o)
		}
	}
	return out, rows.Err()
}

func (t *pgTx) PutObservation(o model.Observation) error {
	_, err := t.tx.Exec(t.ctx, `
		INSERT INTO observations(station_code,date,tmax,tmin,seq,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (station_code,date) DO UPDATE SET
			tmax=EXCLUDED.tmax, tmin=EXCLUDED.tmin,
			seq=EXCLUDED.seq, updated_at=EXCLUDED.updated_at`,
		o.StationCode, o.Date, o.TMax, o.TMin, o.Seq, o.UpdatedAt)
	return err
}

func (t *pgTx) PutStaleObservation(o model.Observation) error {
	_, err := t.tx.Exec(t.ctx, `
		INSERT INTO observations_stale(station_code,date,tmax,tmin,seq,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		o.StationCode, o.Date, o.TMax, o.TMin, o.Seq, o.UpdatedAt)
	return err
}

// ---------- 气候平均 ----------

func (t *pgTx) GetClimate(station string, doy int) (*model.ClimateNormal, error) {
	var c model.ClimateNormal
	err := t.tx.QueryRow(t.ctx,
		`SELECT station_code,doy,tmax,tmin FROM climate_normals
		 WHERE station_code=$1 AND doy=$2`, station, doy).
		Scan(&c.StationCode, &c.DOY, &c.TMax, &c.TMin)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// ListClimateNormals 一次取回给定站点集合的全部 DOY 气候平均（供试走批量预载）。
func (t *pgTx) ListClimateNormals(stations []string) ([]model.ClimateNormal, error) {
	if len(stations) == 0 {
		return nil, nil
	}
	rows, err := t.tx.Query(t.ctx, `
		SELECT station_code,doy,tmax,tmin FROM climate_normals
		WHERE station_code = ANY($1) ORDER BY station_code,doy`, stations)
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
