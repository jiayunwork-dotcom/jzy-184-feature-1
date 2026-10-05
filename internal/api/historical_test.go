package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// seedHistBase 通过 HTTP 建好站、品种、地块，返回可直接复用的 handler。
func seedHistBase(t *testing.T, h http.Handler) {
	t.Helper()
	code, _ := doJSON(t, h, "POST", "/api/v1/stations", map[string]any{
		"code": "S1", "name": "一号站", "latitude": 30.0, "longitude": 100.0, "elevation": 500,
	})
	if code != 200 {
		t.Fatalf("建站失败 %d", code)
	}
	code, _ = doJSON(t, h, "POST", "/api/v1/varieties", map[string]any{
		"code": "V1", "base_temp": 10, "upper_temp": 30,
		"thresholds": []float64{30, 200, 400, 460, 800},
	})
	if code != 200 {
		t.Fatalf("建品种失败 %d", code)
	}
	code, _ = doJSON(t, h, "POST", "/api/v1/plots", map[string]any{
		"code": "P1", "sow_date": "2026-03-01", "variety": "V1",
		"station": "S1", "method": "mean",
	})
	if code != 200 {
		t.Fatalf("建地块失败 %d", code)
	}
}

func histYear(code string, year, n int, tmax, tmin float64) []map[string]any {
	d0 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
	recs := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		recs = append(recs, map[string]any{
			"station_code": code, "year": year,
			"date": d0.AddDate(0, 0, i).Format("2006-01-02"),
			"tmax": tmax, "tmin": tmin,
		})
	}
	return recs
}

func TestHistoricalImportHTTP(t *testing.T) {
	_, h := testRouter(t)
	seedHistBase(t, h)

	// 一整年合法 + 一条非法（不同站年但站不存在）。
	recs := histYear("S1", 2010, 365, 25, 15)
	recs = append(recs, map[string]any{
		"station_code": "GHOST", "year": 2010, "date": "2010-01-01",
		"tmax": 20, "tmin": 10,
	})
	code, body := doJSON(t, h, "POST", "/api/v1/historical/batch", map[string]any{"records": recs})
	if code != 200 {
		t.Fatalf("批量导入应 200（逐条回报），got %d body=%v", code, body)
	}
	data, _ := body["data"].(map[string]any)
	years, _ := data["years"].([]any)
	if len(years) != 2 {
		t.Fatalf("应有 2 个站-年结果，got %d", len(years))
	}
	for _, y := range years {
		ym, _ := y.(map[string]any)
		if ym["station_code"] == "S1" && ym["applied"] != true {
			t.Fatalf("S1/2010 应整体生效：%v", ym)
		}
		if ym["station_code"] == "GHOST" && ym["applied"] != false {
			t.Fatalf("GHOST/2010 应整体不生效：%v", ym)
		}
	}

	// 试走范围。
	code, body = doJSON(t, h, "GET", "/api/v1/plots/P1/outlook?date=2026-06-01", nil)
	if code != 200 || body["ok"] != true {
		t.Fatalf("试走范围查询失败：%d %v", code, body)
	}
	data, _ = body["data"].(map[string]any)
	stages, _ := data["stages"].([]any)
	if len(stages) != 5 {
		t.Fatalf("应返回 5 个阶段，got %d", len(stages))
	}
	mat, _ := stages[4].(map[string]any)
	if mat["stage"] != "maturity" {
		t.Fatalf("最后阶段应为 maturity：%v", mat["stage"])
	}
	if _, ok := mat["n_years"]; !ok {
		t.Fatal("未达到阶段应含 n_years")
	}
	qs, _ := mat["quantiles"].([]any)
	if len(qs) != 3 {
		t.Fatalf("默认应有 3 个分位，got %d", len(qs))
	}

	// 概率查询。
	code, body = doJSON(t, h, "GET",
		"/api/v1/plots/P1/stage-probability?stage=tasseling&by=2026-08-01&date=2026-06-01", nil)
	if code != 200 {
		t.Fatalf("概率查询失败：%d %v", code, body)
	}
	pr, _ := body["data"].(map[string]any)
	if _, ok := pr["proportion"]; !ok {
		t.Fatal("概率响应应含 proportion")
	}
}

func TestOutlookHTTPNoHistoryAndInvalid(t *testing.T) {
	_, h := testRouter(t)
	seedHistBase(t, h)

	// 无历年资料：422 + 明确中文错误，而不是正常的空结果。
	code, body := doJSON(t, h, "GET", "/api/v1/plots/P1/outlook", nil)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("无历年资料应 422，got %d", code)
	}
	errMsg, _ := body["error"].(string)
	if errMsg == "" {
		t.Fatal("应带明确错误信息")
	}

	// 先导一年，再验证非法查询。
	recs := histYear("S1", 2010, 365, 25, 15)
	if code, _ := doJSON(t, h, "POST", "/api/v1/historical/batch",
		map[string]any{"records": recs}); code != 200 {
		t.Fatalf("导入失败 %d", code)
	}
	if code, _ := doJSON(t, h, "GET", "/api/v1/plots/P1/outlook?quantiles=0.5,1.2", nil); code != 400 {
		t.Fatalf("分位越界应 400，got %d", code)
	}
	if code, _ := doJSON(t, h, "GET", "/api/v1/plots/P1/outlook?quantiles=x", nil); code != 400 {
		t.Fatalf("非数字分位应 400，got %d", code)
	}
	if code, _ := doJSON(t, h, "GET", "/api/v1/plots/NOPE/outlook", nil); code != 404 {
		t.Fatalf("地块不存在应 404，got %d", code)
	}
	// 概率：阶段不存在、日期早于播种日、日期格式错。
	if code, _ := doJSON(t, h, "GET",
		"/api/v1/plots/P1/stage-probability?stage=nope&by=2026-08-01", nil); code != 400 {
		t.Fatal("非法阶段应 400")
	}
	if code, _ := doJSON(t, h, "GET",
		"/api/v1/plots/P1/stage-probability?stage=tasseling&by=2026-02-01", nil); code != 400 {
		t.Fatal("日期早于播种日应 400")
	}
	if code, _ := doJSON(t, h, "GET",
		"/api/v1/plots/P1/stage-probability?stage=tasseling&by=bad", nil); code != 400 {
		t.Fatal("非法日期应 400")
	}
}

// TestOldEndpointsUnchangedWithHistorical 导不导历年资料，原有 stages /
// daily / events 接口的字段与单点预测结果都不变（历年资料只喂集合试走）。
func TestOldEndpointsUnchangedWithHistorical(t *testing.T) {
	_, h := testRouter(t)
	seedHistBase(t, h)

	// 录气候平均：25/15 恒温，单点预计按气候平均走（mean GDD=10/天）。
	for doy := 1; doy <= 366; doy++ {
		code, _ := doJSON(t, h, "POST", "/api/v1/climate-normals", map[string]any{
			"station_code": "S1", "doy": doy, "tmax": 25, "tmin": 15,
		})
		if code != 200 {
			t.Fatalf("气候平均 doy=%d 失败 %d", doy, code)
		}
	}

	snapshotStages := func() string {
		code, body := doJSON(t, h, "GET", "/api/v1/plots/P1/stages?date=2026-06-01", nil)
		if code != 200 {
			t.Fatalf("stages 查询失败 %d", code)
		}
		b, _ := json.Marshal(body["data"])
		return string(b)
	}
	before := snapshotStages()

	// 事件计数取在导历年之前：气候平均逐条写入本身会产生阶段事件，
	// 那些与历史无关，只验证“导历史”这一步不新增事件。
	eventCount := func() int {
		code, body := doJSON(t, h, "GET", "/api/v1/events?limit=500", nil)
		if code != 200 {
			t.Fatalf("事件查询失败 %d", code)
		}
		evs, _ := body["data"].([]any)
		return len(evs)
	}
	evBefore := eventCount()

	// 导入与气候平均完全相同的多年历史，再导入极热、极冷年。
	recs := histYear("S1", 2001, 365, 25, 15)
	recs = append(recs, histYear("S1", 2002, 365, 42, 35)...)
	recs = append(recs, histYear("S1", 2003, 365, 12, 8)...)
	if code, _ := doJSON(t, h, "POST", "/api/v1/historical/batch",
		map[string]any{"records": recs}); code != 200 {
		t.Fatalf("导入历史失败 %d", code)
	}

	after := snapshotStages()
	if before != after {
		t.Fatalf("单点预测不应被历年资料改变：\nbefore=%s\nafter =%s", before, after)
	}

	// 导历年资料这一步不应产生任何阶段事件。
	if n := eventCount(); n != evBefore {
		t.Fatalf("导历年资料不应产生阶段事件：%d -> %d", evBefore, n)
	}

	// daily 行数与字段仍正常（270 窗口，asOf 2026-06-01）。
	code, body := doJSON(t, h, "GET", "/api/v1/plots/P1/daily?date=2026-06-01", nil)
	if code != 200 {
		t.Fatalf("daily 查询失败 %d", code)
	}
	rows, _ := body["data"].([]any)
	if len(rows) == 0 {
		t.Fatal("daily 不应为空")
	}
	first, _ := rows[0].(map[string]any)
	for _, k := range []string{"date", "tmax", "tmin", "gdd", "cumulative", "source"} {
		if _, ok := first[k]; !ok {
			t.Fatalf("daily 原字段 %s 缺失", k)
		}
	}
}
