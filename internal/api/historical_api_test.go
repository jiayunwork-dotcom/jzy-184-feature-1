package api

import (
	"net/http"
	"testing"
	"time"
)

// buildHistYear 构造一整年的历史记录 JSON 数组。
func buildHistYear(station string, year int, tmax, tmin float64) []map[string]any {
	out := make([]map[string]any, 0, 365)
	d := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	for d.Year() == year {
		out = append(out, map[string]any{
			"station_code": station,
			"date":         d.Format("2006-01-02"),
			"tmax":         tmax,
			"tmin":         tmin,
		})
		d = d.AddDate(0, 0, 1)
	}
	return out
}

// TestHistoricalHTTP 端到端：整年导入 → 范围/分位 → 比例 → 非法查询拒绝。
func TestHistoricalHTTP(t *testing.T) {
	_, h := testRouter(t)

	post := func(path string, body any) (int, map[string]any) {
		return doJSON(t, h, "POST", path, body)
	}
	get := func(path string) (int, map[string]any) {
		return doJSON(t, h, "GET", path, nil)
	}
	expect := func(t *testing.T, c, want int, body map[string]any) {
		t.Helper()
		if c != want {
			t.Fatalf("期望 %d 得到 %d：%v", want, c, body)
		}
	}

	c, b := post("/api/v1/stations", map[string]any{
		"code": "S1", "latitude": 30.0, "longitude": 100.0, "elevation": 500,
	})
	expect(t, c, 200, b)
	c, b = post("/api/v1/varieties", map[string]any{
		"code": "V1", "base_temp": 10, "upper_temp": 30,
		"thresholds": []float64{100, 400, 900, 1400, 2000},
	})
	expect(t, c, 200, b)
	c, b = post("/api/v1/plots", map[string]any{
		"code": "P1", "sow_date": "2026-03-01", "variety": "V1",
		"station": "S1", "method": "mean",
	})
	expect(t, c, 200, b)
	// 气候平均（GDD 10/天）。
	for doy := 1; doy <= 366; doy++ {
		c, _ := post("/api/v1/climate-normals", map[string]any{
			"station_code": "S1", "doy": doy, "tmax": 25, "tmin": 15,
		})
		if c != 200 {
			t.Fatalf("写气候平均失败 doy=%d code=%d", doy, c)
		}
	}

	// 无历年：范围明确不可用，不是看着正常的空结果。
	c, b = get("/api/v1/plots/P1/stage-ranges?date=2026-03-01")
	expect(t, c, 200, b)
	data, _ := b["data"].(map[string]any)
	if data["available"] != false || data["reason"] == "" {
		t.Fatalf("无历年应 available=false 且带原因：%v", data)
	}

	// 导入 5 个恒温年：GDD 6..10/天。
	for i, y := 0, 2000; i < 5; i, y = i+1, y+1 {
		g := 6.0 + float64(i) // 6,7,8,9,10
		tmax, tmin := 10+g+4, 10+g-4
		c, b := post("/api/v1/historical/batch",
			map[string]any{"records": buildHistYear("S1", y, tmax, tmin)})
		expect(t, c, 200, b)
		d, _ := b["data"].(map[string]any)
		items, _ := d["items"].([]any)
		for _, it := range items {
			im, _ := it.(map[string]any)
			if im["applied"] != true {
				t.Fatalf("%d 年记录应 applied：%v", y, im)
			}
		}
	}

	// 范围查询。
	c, b = get("/api/v1/plots/P1/stage-ranges?date=2026-03-01&quantiles=0.1,0.5,0.9")
	expect(t, c, 200, b)
	data, _ = b["data"].(map[string]any)
	if data["available"] != true {
		t.Fatalf("导入后应可用：%v", data)
	}
	ranges, _ := data["ranges"].([]any)
	if len(ranges) != 5 {
		t.Fatalf("应 5 个阶段，got %d", len(ranges))
	}
	for _, rr := range ranges {
		rm, _ := rr.(map[string]any)
		if n, _ := rm["years_used"].(float64); n != 5 {
			t.Fatalf("应用 5 年：%v", rm["years_used"])
		}
		if rm["reached"] != false {
			t.Fatalf("asOf=播种日时阶段都未达到")
		}
	}

	// 非法分位：400。
	expect(t, mustStatus(get("/api/v1/plots/P1/stage-ranges?quantiles=1.2&date=2026-03-01")), http.StatusBadRequest, nil)
	expect(t, mustStatus(get("/api/v1/plots/P1/stage-ranges?quantiles=abc&date=2026-03-01")), http.StatusBadRequest, nil)
	// 非法阶段名 / 早于播种日 / 缺 by：400。
	expect(t, mustStatus(get("/api/v1/plots/P1/arrival?stage=flower&by=2026-08-01&date=2026-03-01")), http.StatusBadRequest, nil)
	expect(t, mustStatus(get("/api/v1/plots/P1/arrival?stage=tasseling&by=2026-02-01&date=2026-03-01")), http.StatusBadRequest, nil)
	expect(t, mustStatus(get("/api/v1/plots/P1/arrival?stage=tasseling&date=2026-03-01")), http.StatusBadRequest, nil)

	// 合法比例查询：出苗阈值 100。最快年 GDD 10 → 第 10 天累计 100 达到，
	// 此时仅 1/5 = 0.2。
	c, b = get("/api/v1/plots/P1/arrival?stage=emergence&by=2026-03-10&date=2026-03-01")
	expect(t, c, 200, b)
	ad, _ := b["data"].(map[string]any)
	if fr, _ := ad["fraction"].(float64); fr < 0.19 || fr > 0.21 {
		t.Fatalf("3/10 出苗比例应约 0.2，got %v", ad["fraction"])
	}
	if ad["available"] != true {
		t.Fatalf("应可用")
	}

	// 导入非法记录：脏站年整体不生效，逐条回报。
	c, b = post("/api/v1/historical/batch", map[string]any{
		"records": []map[string]any{
			{"station_code": "S1", "date": "2001-06-01", "tmax": 25, "tmin": 15},
			{"station_code": "S1", "date": "2001-06-02", "tmax": 10, "tmin": 20},
		},
	})
	expect(t, c, 200, b)
	d, _ := b["data"].(map[string]any)
	items, _ := d["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("应逐条回报 2 条")
	}
	first, _ := items[0].(map[string]any)
	second, _ := items[1].(map[string]any)
	if first["applied"] != false {
		t.Fatalf("脏站年合法条不应 applied：%v", first)
	}
	if second["ok"] != false || second["reason"] == "" {
		t.Fatalf("非法条应 ok=false 带原因：%v", second)
	}
}

func mustStatus(c int, _ map[string]any) int { return c }
