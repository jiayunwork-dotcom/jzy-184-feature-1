-- 历年逐日气温资料（用于集合试走：把预测窗口剩下的日子各历史年试走一遍）。
-- 与当年观测 observations 分开：历年资料不参与单点预测、逐日曲线与阶段
-- 事件。按 (station_code, date) 唯一；year 冗余列供“一站一年整年替换”
-- 与按年索引用。整年替换在一个事务内 DELETE+INSERT，崩溃整体回滚，
-- 任何查询都看不到导了一半的一年。
CREATE TABLE IF NOT EXISTS historical_temps (
    station_code TEXT NOT NULL REFERENCES stations(code),
    year         INTEGER NOT NULL CHECK (year BETWEEN 1900 AND 2200),
    date         DATE NOT NULL,
    tmax         DOUBLE PRECISION NOT NULL,
    tmin         DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (station_code, date),
    CONSTRAINT hist_temp_order CHECK (tmin <= tmax),
    CONSTRAINT hist_temp_range CHECK (tmin BETWEEN -60 AND 65 AND tmax BETWEEN -60 AND 65),
    CONSTRAINT hist_date_year CHECK (EXTRACT(YEAR FROM date)::INTEGER = year)
);
CREATE INDEX IF NOT EXISTS idx_hist_station_year ON historical_temps(station_code, year);
