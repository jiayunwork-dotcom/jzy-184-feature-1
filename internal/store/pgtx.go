package store

import (
	"context"
	"hash/fnv"
	"time"

	"agristation/internal/model"

	"github.com/jackc/pgx/v5"
)

// pgTx 实现 engine.Tx。
type pgTx struct {
	tx  pgx.Tx
	ctx context.Context
}

// LockStations 对站代码哈希后依次加事务级排他咨询锁。
// codes 已由调用方排序，保证多站加锁顺序一致，避免死锁。
func (t *pgTx) LockStations(codes []string) error {
	for _, c := range codes {
		h := fnv.New64a()
		_, _ = h.Write([]byte(c))
		// 映射到有符号 bigint。
		v := int64(h.Sum64())
		if _, err := t.tx.Exec(t.ctx,
			`SELECT pg_advisory_xact_lock($1)`, v); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 站点 ----------

func (t *pgTx) PutStation(s model.Station) error {
	_, err := t.tx.Exec(t.ctx, `
		INSERT INTO stations(code, name, latitude, longitude, elevation, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (code) DO UPDATE SET
			name=EXCLUDED.name, latitude=EXCLUDED.latitude,
			longitude=EXCLUDED.longitude, elevation=EXCLUDED.elevation,
			updated_at=EXCLUDED.updated_at`,
		s.Code, s.Name, s.Latitude, s.Longitude, s.Elevation, s.CreatedAt, s.UpdatedAt)
	return err
}

func (t *pgTx) scanStation(row pgx.Row) (*model.Station, error) {
	var s model.Station
	err := row.Scan(&s.Code, &s.Name, &s.Latitude, &s.Longitude, &s.Elevation, &s.CreatedAt, &s.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (t *pgTx) GetStation(code string) (*model.Station, error) {
	return t.scanStation(t.tx.QueryRow(t.ctx,
		`SELECT code,name,latitude,longitude,elevation,created_at,updated_at
		 FROM stations WHERE code=$1`, code))
}

func (t *pgTx) ListStations() ([]model.Station, error) {
	rows, err := t.tx.Query(t.ctx,
		`SELECT code,name,latitude,longitude,elevation,created_at,updated_at
		 FROM stations ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Station
	for rows.Next() {
		var s model.Station
		if err := rows.Scan(&s.Code, &s.Name, &s.Latitude, &s.Longitude, &s.Elevation, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------- 品种 ----------

func (t *pgTx) PutVariety(v model.Variety) error {
	_, err := t.tx.Exec(t.ctx, `
		INSERT INTO varieties(code,name,base_temp,upper_temp,thresholds,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (code) DO UPDATE SET
			name=EXCLUDED.name, base_temp=EXCLUDED.base_temp,
			upper_temp=EXCLUDED.upper_temp, thresholds=EXCLUDED.thresholds,
			updated_at=EXCLUDED.updated_at`,
		v.Code, v.Name, v.BaseTemp, v.UpperTemp, v.Thresholds, v.CreatedAt, v.UpdatedAt)
	return err
}

func (t *pgTx) GetVariety(code string) (*model.Variety, error) {
	var v model.Variety
	err := t.tx.QueryRow(t.ctx,
		`SELECT code,name,base_temp,upper_temp,thresholds,created_at,updated_at
		 FROM varieties WHERE code=$1`, code).
		Scan(&v.Code, &v.Name, &v.BaseTemp, &v.UpperTemp, &v.Thresholds, &v.CreatedAt, &v.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// ---------- 地块与绑定 ----------

func (t *pgTx) PutPlot(p model.Plot) error {
	_, err := t.tx.Exec(t.ctx, `
		INSERT INTO plots(code,name,sow_date,variety,method,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (code) DO UPDATE SET
			name=EXCLUDED.name, sow_date=EXCLUDED.sow_date,
			variety=EXCLUDED.variety, method=EXCLUDED.method,
			updated_at=EXCLUDED.updated_at`,
		p.Code, p.Name, p.SowDate, p.Variety, p.Method, p.CreatedAt, p.UpdatedAt)
	return err
}

func (t *pgTx) GetPlot(code string) (*model.Plot, error) {
	var p model.Plot
	err := t.tx.QueryRow(t.ctx,
		`SELECT code,name,sow_date,variety,method,created_at,updated_at
		 FROM plots WHERE code=$1`, code).
		Scan(&p.Code, &p.Name, &p.SowDate, &p.Variety, &p.Method, &p.CreatedAt, &p.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (t *pgTx) ListPlots() ([]model.Plot, error) {
	rows, err := t.tx.Query(t.ctx,
		`SELECT code,name,sow_date,variety,method,created_at,updated_at
		 FROM plots ORDER BY code`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Plot
	for rows.Next() {
		var p model.Plot
		if err := rows.Scan(&p.Code, &p.Name, &p.SowDate, &p.Variety, &p.Method, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (t *pgTx) AddBinding(b model.Binding) error {
	_, err := t.tx.Exec(t.ctx, `
		INSERT INTO plot_bindings(plot_code, station_code, effective_date, created_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (plot_code, effective_date) DO UPDATE SET
			station_code=EXCLUDED.station_code, created_at=EXCLUDED.created_at`,
		b.PlotCode, b.StationCode, b.EffectiveDate, b.CreatedAt)
	return err
}

func (t *pgTx) ListBindings(plot string) ([]model.Binding, error) {
	rows, err := t.tx.Query(t.ctx,
		`SELECT plot_code,station_code,effective_date,created_at
		 FROM plot_bindings WHERE plot_code=$1
		 ORDER BY effective_date`, plot)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Binding
	for rows.Next() {
		var b model.Binding
		var ed time.Time
		if err := rows.Scan(&b.PlotCode, &b.StationCode, &ed, &b.CreatedAt); err != nil {
			return nil, err
		}
		b.EffectiveDate = dateOnly(ed)
		out = append(out, b)
	}
	return out, rows.Err()
}

// dateOnly 把数据库读出的 DATE（可能带时区/位置）统一成 UTC 零点。
func dateOnly(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func (t *pgTx) PutClimateNormal(c model.ClimateNormal) error {
	_, err := t.tx.Exec(t.ctx, `
		INSERT INTO climate_normals(station_code,doy,tmax,tmin)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (station_code,doy) DO UPDATE SET
			tmax=EXCLUDED.tmax, tmin=EXCLUDED.tmin`,
		c.StationCode, c.DOY, c.TMax, c.TMin)
	return err
}
