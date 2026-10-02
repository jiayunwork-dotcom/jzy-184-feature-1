-- 玉米积温预测服务初始结构（PostgreSQL 16）

CREATE TABLE IF NOT EXISTS stations (
    code        TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    latitude    DOUBLE PRECISION NOT NULL,
    longitude   DOUBLE PRECISION NOT NULL,
    elevation   DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS varieties (
    code        TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    base_temp   DOUBLE PRECISION NOT NULL,
    upper_temp  DOUBLE PRECISION NOT NULL,
    -- 出苗、拔节、抽雄、吐丝、成熟五个累计积温需求，严格递增
    thresholds  DOUBLE PRECISION[] NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,
    CONSTRAINT varieties_base_below_upper CHECK (base_temp < upper_temp)
);

CREATE TABLE IF NOT EXISTS plots (
    code        TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    sow_date    DATE NOT NULL,
    variety     TEXT NOT NULL REFERENCES varieties(code),
    method      TEXT NOT NULL DEFAULT 'sine',
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,
    CONSTRAINT plots_method_check CHECK (method IN ('sine', 'triangle', 'mean'))
);

-- 地块-站点绑定，一行表示“自 effective_date 起改绑到 station_code”。
-- 同一地块同一生效日只允许一条（重复改绑以最新一条为准，见 updated_at）。
CREATE TABLE IF NOT EXISTS plot_bindings (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    plot_code      TEXT NOT NULL REFERENCES plots(code),
    station_code   TEXT NOT NULL REFERENCES stations(code),
    effective_date DATE NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL,
    UNIQUE (plot_code, effective_date)
);
CREATE INDEX IF NOT EXISTS idx_bindings_plot ON plot_bindings(plot_code, effective_date);

-- 每个站日只保留“胜出”的一条（序号最大）。
CREATE TABLE IF NOT EXISTS observations (
    station_code TEXT NOT NULL,
    date         DATE NOT NULL,
    tmax         DOUBLE PRECISION NOT NULL,
    tmin         DOUBLE PRECISION NOT NULL,
    seq          INTEGER NOT NULL CHECK (seq > 0),
    updated_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (station_code, date),
    CONSTRAINT obs_temp_order CHECK (tmin <= tmax)
);
CREATE INDEX IF NOT EXISTS idx_observations_date ON observations(date);

-- 被更大序号压制的晚到/历史记录只存档，不参与计算。
CREATE TABLE IF NOT EXISTS observations_stale (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    station_code TEXT NOT NULL,
    date         DATE NOT NULL,
    tmax         DOUBLE PRECISION NOT NULL,
    tmin         DOUBLE PRECISION NOT NULL,
    seq          INTEGER NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    archived_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_stale_station_date ON observations_stale(station_code, date);

-- 站点历年同日（DOY 1..366）气候平均。
CREATE TABLE IF NOT EXISTS climate_normals (
    station_code TEXT NOT NULL REFERENCES stations(code),
    doy          INTEGER NOT NULL CHECK (doy BETWEEN 1 AND 366),
    tmax         DOUBLE PRECISION NOT NULL,
    tmin         DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (station_code, doy),
    CONSTRAINT normals_temp_order CHECK (tmin <= tmax)
);

-- 逐地块逐日快照：当日温度（缺测为 NULL）、日积温、累计积温、来源标记。
-- (plot_code, date) 唯一，增量重算时删除 from 起的行后整段重写。
CREATE TABLE IF NOT EXISTS plot_daily (
    plot_code   TEXT NOT NULL REFERENCES plots(code),
    date        DATE NOT NULL,
    tmax        DOUBLE PRECISION,
    tmin        DOUBLE PRECISION,
    gdd         DOUBLE PRECISION NOT NULL CHECK (gdd >= 0),
    cumulative  DOUBLE PRECISION NOT NULL,
    source      TEXT NOT NULL CHECK (source IN ('observed', 'filled', 'climate', 'missing')),
    fill_method TEXT NOT NULL DEFAULT '',
    fill_from   TEXT,
    as_of       DATE NOT NULL,
    PRIMARY KEY (plot_code, date)
);
CREATE INDEX IF NOT EXISTS idx_plot_daily_date ON plot_daily(plot_code, date);

-- 每地块每阶段一行的当前结果。
CREATE TABLE IF NOT EXISTS plot_stages (
    plot_code   TEXT NOT NULL REFERENCES plots(code),
    stage       TEXT NOT NULL,
    threshold   DOUBLE PRECISION NOT NULL,
    status      TEXT NOT NULL DEFAULT '' CHECK (status IN ('', 'reached', 'forecast')),
    date        DATE,
    cumulative  DOUBLE PRECISION,
    as_of       DATE NOT NULL,
    PRIMARY KEY (plot_code, stage)
);

-- 阶段日期变更事件。event_id 含 change_id，幂等去重。
CREATE TABLE IF NOT EXISTS stage_events (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id    TEXT NOT NULL UNIQUE,
    plot_code   TEXT NOT NULL,
    stage       TEXT NOT NULL,
    old_date    DATE,
    new_date    DATE,
    old_status  TEXT NOT NULL DEFAULT '',
    new_status  TEXT NOT NULL DEFAULT '',
    change_id   TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    occurred_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_id ON stage_events(id);
CREATE INDEX IF NOT EXISTS idx_events_plot_time ON stage_events(plot_code, id);
