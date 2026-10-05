-- 历年逐日气温（用于把往年“试走”一遍做年际范围推演）。
-- 一站一年是一个整体导入单元：导入在一个事务内整年替换，
-- 任何时刻都查不到“导了一半”的一年（见 historical_ingest.go）。
CREATE TABLE IF NOT EXISTS historical_weather (
    station_code TEXT NOT NULL REFERENCES stations(code),
    year         INTEGER NOT NULL CHECK (year BETWEEN 1900 AND 2100),
    date         DATE NOT NULL,
    tmax         DOUBLE PRECISION NOT NULL,
    tmin         DOUBLE PRECISION NOT NULL,
    imported_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (station_code, date),
    CONSTRAINT hist_temp_order CHECK (tmin <= tmax),
    CONSTRAINT hist_temp_range CHECK (tmin BETWEEN -60 AND 65 AND tmax BETWEEN -60 AND 65),
    CONSTRAINT hist_date_year CHECK (EXTRACT(YEAR FROM date)::INTEGER = year)
);
CREATE INDEX IF NOT EXISTS idx_hist_station_year ON historical_weather(station_code, year, date);
