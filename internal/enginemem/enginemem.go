package enginemem

import (
	"context"
	"sort"
	"sync"
	"time"

	"agristation/internal/model"
)

// memStore 是测试用的内存事务实现。整库一把互斥锁（比 PG 的按站
// advisory lock 更严格），足以验证引擎的仲裁与“只算一次”语义。
//
// Update 在共享状态上执行，fn 返回 error 时用快照回滚，语义与数据库事务一致。
type Store struct {
	mu         sync.Mutex
	stations   map[string]model.Station
	varieties  map[string]model.Variety
	plots      map[string]model.Plot
	bindings   map[string][]model.Binding // plot -> 按生效日
	obs        map[obsKey]model.Observation
	stale      []model.Observation
	normals    map[normalKey]model.ClimateNormal
	historical map[histKey]model.HistoricalWeather
	daily      map[string][]model.DailyValue // plot -> 按日期
	stages     map[string][]model.StageDate
	events     []model.StageEvent
	eventByID  map[string]int64
	eventSeq   int64
	// UpdateCount 记录成功提交的写事务数（并发测试辅助）。
	UpdateCount int
}

type obsKey struct {
	st string
	d  time.Time
}
type normalKey struct {
	st  string
	doy int
}
type histKey struct {
	st string
	d  time.Time
}

func New() *Store {
	return &Store{
		stations:   map[string]model.Station{},
		varieties:  map[string]model.Variety{},
		plots:      map[string]model.Plot{},
		bindings:   map[string][]model.Binding{},
		obs:        map[obsKey]model.Observation{},
		normals:    map[normalKey]model.ClimateNormal{},
		historical: map[histKey]model.HistoricalWeather{},
		daily:      map[string][]model.DailyValue{},
		stages:     map[string][]model.StageDate{},
		eventByID:  map[string]int64{},
	}
}

func dateKey(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// snapshot 用于事务回滚。
type memSnapshot struct {
	stations   map[string]model.Station
	varieties  map[string]model.Variety
	plots      map[string]model.Plot
	bindings   map[string][]model.Binding
	obs        map[obsKey]model.Observation
	stale      []model.Observation
	normals    map[normalKey]model.ClimateNormal
	historical map[histKey]model.HistoricalWeather
	daily      map[string][]model.DailyValue
	stages     map[string][]model.StageDate
	events     []model.StageEvent
	eventByID  map[string]int64
	eventSeq   int64
}

func (m *Store) snapshotState() memSnapshot {
	s := memSnapshot{eventSeq: m.eventSeq}
	s.stations = cloneMap(m.stations)
	s.varieties = cloneMap(m.varieties)
	s.plots = cloneMap(m.plots)
	s.bindings = map[string][]model.Binding{}
	for k, v := range m.bindings {
		s.bindings[k] = append([]model.Binding(nil), v...)
	}
	s.obs = cloneMap(m.obs)
	s.stale = append([]model.Observation(nil), m.stale...)
	s.normals = cloneMap(m.normals)
	s.historical = cloneMap(m.historical)
	s.daily = map[string][]model.DailyValue{}
	for k, v := range m.daily {
		s.daily[k] = append([]model.DailyValue(nil), v...)
	}
	s.stages = map[string][]model.StageDate{}
	for k, v := range m.stages {
		s.stages[k] = append([]model.StageDate(nil), v...)
	}
	s.events = append([]model.StageEvent(nil), m.events...)
	s.eventByID = cloneMap(m.eventByID)
	return s
}

func cloneMap[K comparable, V any](src map[K]V) map[K]V {
	dst := make(map[K]V, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func (m *Store) restore(s memSnapshot) {
	m.stations, m.varieties, m.plots = s.stations, s.varieties, s.plots
	m.bindings, m.obs, m.stale = s.bindings, s.obs, s.stale
	m.normals, m.historical = s.normals, s.historical
	m.daily, m.stages = s.daily, s.stages
	m.events, m.eventByID, m.eventSeq = s.events, s.eventByID, s.eventSeq
}

func (m *Store) Update(_ context.Context, fn func(*memTx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.snapshotState()
	if err := fn(&memTx{m: m}); err != nil {
		m.restore(snap)
		return err
	}
	m.UpdateCount++
	return nil
}

func (m *Store) View(_ context.Context, fn func(*memTx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(&memTx{m: m})
}

// memTx 的方法直接操作 memStore（在锁内调用）。
type memTx struct{ m *Store }

func (t *memTx) LockStations([]string) error { return nil }

func (t *memTx) GetStation(code string) (*model.Station, error) {
	if s, ok := t.m.stations[code]; ok {
		cp := s
		return &cp, nil
	}
	return nil, nil
}
func (t *memTx) ListStations() ([]model.Station, error) {
	out := make([]model.Station, 0, len(t.m.stations))
	for _, s := range t.m.stations {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}
func (t *memTx) PutStation(s model.Station) error {
	t.m.stations[s.Code] = s
	return nil
}

func (t *memTx) GetVariety(code string) (*model.Variety, error) {
	if v, ok := t.m.varieties[code]; ok {
		cp := v
		cp.Thresholds = append([]float64(nil), v.Thresholds...)
		return &cp, nil
	}
	return nil, nil
}
func (t *memTx) PutVariety(v model.Variety) error {
	cp := v
	cp.Thresholds = append([]float64(nil), v.Thresholds...)
	t.m.varieties[v.Code] = cp
	return nil
}

func (t *memTx) GetPlot(code string) (*model.Plot, error) {
	if p, ok := t.m.plots[code]; ok {
		cp := p
		return &cp, nil
	}
	return nil, nil
}
func (t *memTx) ListPlots() ([]model.Plot, error) {
	out := make([]model.Plot, 0, len(t.m.plots))
	for _, p := range t.m.plots {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}
func (t *memTx) PutPlot(p model.Plot) error {
	t.m.plots[p.Code] = p
	if _, ok := t.m.bindings[p.Code]; !ok {
		t.m.bindings[p.Code] = nil
	}
	return nil
}

func (t *memTx) ListBindings(plot string) ([]model.Binding, error) {
	out := append([]model.Binding(nil), t.m.bindings[plot]...)
	sort.Slice(out, func(i, j int) bool { return out[i].EffectiveDate.Before(out[j].EffectiveDate) })
	return out, nil
}
func (t *memTx) AddBinding(b model.Binding) error {
	bs := t.m.bindings[b.PlotCode]
	for i, x := range bs {
		if x.EffectiveDate.Equal(b.EffectiveDate) {
			bs[i] = b
			t.m.bindings[b.PlotCode] = bs
			return nil
		}
	}
	bs = append(bs, b)
	sort.Slice(bs, func(i, j int) bool { return bs[i].EffectiveDate.Before(bs[j].EffectiveDate) })
	t.m.bindings[b.PlotCode] = bs
	return nil
}

func (t *memTx) PutClimateNormal(c model.ClimateNormal) error {
	t.m.normals[normalKey{c.StationCode, c.DOY}] = c
	return nil
}

func (t *memTx) GetObservation(station string, d model.Date) (*model.Observation, error) {
	if o, ok := t.m.obs[obsKey{station, dateKey(d)}]; ok {
		cp := o
		return &cp, nil
	}
	return nil, nil
}
func (t *memTx) ListObservationsOnDate(d model.Date) ([]model.Observation, error) {
	var out []model.Observation
	for _, o := range t.m.obs {
		if o.Date.Equal(d) {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StationCode < out[j].StationCode })
	return out, nil
}
func (t *memTx) PutObservation(o model.Observation) error {
	t.m.obs[obsKey{o.StationCode, dateKey(o.Date)}] = o
	return nil
}
func (t *memTx) PutStaleObservation(o model.Observation) error {
	t.m.stale = append(t.m.stale, o)
	return nil
}
func (t *memTx) GetClimate(station string, doy int) (*model.ClimateNormal, error) {
	if n, ok := t.m.normals[normalKey{station, doy}]; ok {
		cp := n
		return &cp, nil
	}
	return nil, nil
}

func (t *memTx) ListClimateNormals(stations []string) ([]model.ClimateNormal, error) {
	want := map[string]bool{}
	for _, s := range stations {
		want[s] = true
	}
	out := make([]model.ClimateNormal, 0)
	for _, n := range t.m.normals {
		if want[n.StationCode] {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StationCode != out[j].StationCode {
			return out[i].StationCode < out[j].StationCode
		}
		return out[i].DOY < out[j].DOY
	})
	return out, nil
}

// ---------- 历年逐日气温 ----------

func (t *memTx) ListHistoricalYears(station string) ([]int, error) {
	seen := map[int]struct{}{}
	for k := range t.m.historical {
		if k.st == station {
			seen[k.d.Year()] = struct{}{}
		}
	}
	out := make([]int, 0, len(seen))
	for y := range seen {
		out = append(out, y)
	}
	sort.Ints(out)
	return out, nil
}

func (t *memTx) ListHistoricalStationYear(station string, year int) ([]model.HistoricalWeather, error) {
	out := make([]model.HistoricalWeather, 0)
	for _, h := range t.m.historical {
		if h.StationCode == station && h.Year == year {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

func (t *memTx) ListHistorical(stations []string, from, to model.Date) ([]model.HistoricalWeather, error) {
	want := map[string]bool{}
	for _, s := range stations {
		want[s] = true
	}
	out := make([]model.HistoricalWeather, 0)
	for _, h := range t.m.historical {
		if !want[h.StationCode] || h.Date.Before(from) || h.Date.After(to) {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

func (t *memTx) ReplaceHistoricalStationYear(station string, year int, rows []model.HistoricalWeather) error {
	for k := range t.m.historical {
		if k.st == station && k.d.Year() == year {
			delete(t.m.historical, k)
		}
	}
	for _, r := range rows {
		t.m.historical[histKey{r.StationCode, dateKey(r.Date)}] = r
	}
	return nil
}

func (t *memTx) CumulativeBefore(plot string, d model.Date) (float64, error) {
	rows := t.m.daily[plot]
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].Date.Before(d) {
			return rows[i].Cumulative, nil
		}
	}
	return 0, nil
}

func (t *memTx) ReplaceDailyFrom(plot string, from model.Date, rows []model.DailyValue) error {
	existing := t.m.daily[plot]
	kept := make([]model.DailyValue, 0)
	for _, r := range existing {
		if r.Date.Before(from) {
			kept = append(kept, r)
		}
	}
	out := append(kept, rows...)
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	t.m.daily[plot] = out
	return nil
}

func (t *memTx) ListDaily(plot string) ([]model.DailyValue, error) {
	return append([]model.DailyValue(nil), t.m.daily[plot]...), nil
}

func (t *memTx) ReplaceStageDates(plot string, rows []model.StageDate) ([]model.StageDate, error) {
	old := append([]model.StageDate(nil), t.m.stages[plot]...)
	cp := append([]model.StageDate(nil), rows...)
	t.m.stages[plot] = cp
	return old, nil
}

func (t *memTx) InsertEvent(e model.StageEvent) (bool, error) {
	if _, exists := t.m.eventByID[e.EventID]; exists {
		return false, nil
	}
	t.m.eventSeq++
	e.ID = t.m.eventSeq
	t.m.eventByID[e.EventID] = e.ID
	t.m.events = append(t.m.events, e)
	return true, nil
}

func (t *memTx) ListEventsAfter(afterID int64, limit int, plot string) ([]model.StageEvent, error) {
	out := make([]model.StageEvent, 0)
	for _, e := range t.m.events {
		if e.ID <= afterID {
			continue
		}
		if plot != "" && e.PlotCode != plot {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
