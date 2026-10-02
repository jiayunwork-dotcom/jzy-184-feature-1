package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"agristation/internal/engine"
	"agristation/internal/enginemem"
)

func testRouter(t *testing.T) (*engine.Service[enginemem.Tx], http.Handler) {
	t.Helper()
	return newTestSetup(t)
}

func newTestSetup(t *testing.T) (*engine.Service[enginemem.Tx], http.Handler) {
	t.Helper()
	mem := enginemem.New()
	svc := engine.NewService[enginemem.Tx](mem)
	return svc, NewServer[enginemem.Tx](svc).Router()
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	if w.Body.Len() > 0 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("响应不是 JSON：%s", w.Body.String())
		}
	}
	return w.Code, out
}

func TestEndToEndHTTP(t *testing.T) {
	_, h := testRouter(t)

	// 登记站点。
	code, body := doJSON(t, h, "POST", "/api/v1/stations", map[string]any{
		"code": "S1", "name": "一号站", "latitude": 30.0, "longitude": 100.0, "elevation": 500,
	})
	if code != 200 || body["ok"] != true {
		t.Fatalf("登记站点失败：%d %v", code, body)
	}
	// 登记品种：基点不低于上限应 400。
	code, _ = doJSON(t, h, "POST", "/api/v1/varieties", map[string]any{
		"code": "V1", "base_temp": 30, "upper_temp": 20,
		"thresholds": []float64{1, 2, 3, 4, 5},
	})
	if code != 400 {
		t.Fatalf("非法品种应 400，got %d", code)
	}
	// 阶段需求不递增应 400。
	code, _ = doJSON(t, h, "POST", "/api/v1/varieties", map[string]any{
		"code": "V1", "base_temp": 10, "upper_temp": 30,
		"thresholds": []float64{1, 3, 2, 4, 5},
	})
	if code != 400 {
		t.Fatalf("非递增需求应 400，got %d", code)
	}
	// 合法品种。
	code, _ = doJSON(t, h, "POST", "/api/v1/varieties", map[string]any{
		"code": "V1", "name": "品种甲", "base_temp": 10, "upper_temp": 30,
		"thresholds": []float64{30, 200, 400, 460, 800},
	})
	if code != 200 {
		t.Fatalf("合法品种应 200，got %d", code)
	}
	// 登记地块。
	code, _ = doJSON(t, h, "POST", "/api/v1/plots", map[string]any{
		"code": "P1", "sow_date": "2026-03-01", "variety": "V1",
		"station": "S1", "method": "sine",
	})
	if code != 200 {
		t.Fatalf("登记地块应 200，got %d", code)
	}
	// 改绑到不存在的站。
	code, body = doJSON(t, h, "POST", "/api/v1/plots/P1/bindings", map[string]any{
		"station_code": "GHOST", "effective_date": "2026-04-01",
	})
	if code != 400 {
		t.Fatalf("改绑不存在站应 400，got %d body=%v", code, body)
	}
	// 批量观测：夹一条非法。
	code, body = doJSON(t, h, "POST", "/api/v1/observations/batch", map[string]any{
		"records": []map[string]any{
			{"station_code": "S1", "date": "2026-03-02", "tmax": 30, "tmin": 14, "seq": 1},
			{"station_code": "S1", "date": "2026-03-03", "tmax": 10, "tmin": 20, "seq": 1},
		},
	})
	if code != 200 {
		t.Fatalf("批量写入应整体 200，got %d", code)
	}
	data, _ := body["data"].(map[string]any)
	items, _ := data["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("应逐条回报 2 条，got %d", len(items))
	}
	first, _ := items[0].(map[string]any)
	second, _ := items[1].(map[string]any)
	if first["ok"] != true || first["applied"] != true {
		t.Fatalf("合法条应生效：%v", first)
	}
	if second["ok"] != false || second["reason"] == "" {
		t.Fatalf("非法条应失败且带原因：%v", second)
	}

	// 逐日积温：3/2 应为 12。
	code, body = doJSON(t, h, "GET", "/api/v1/plots/P1/daily", nil)
	if code != 200 {
		t.Fatalf("查询逐日曲线失败：%d", code)
	}
	rows, _ := body["data"].([]any)
	var found bool
	for _, r := range rows {
		rm, _ := r.(map[string]any)
		if rm["date"] != nil && rm["date"].(string) == "2026-03-02T00:00:00Z" {
			if gdd, _ := rm["gdd"].(float64); gdd != 12 {
				t.Fatalf("3/2 积温应为 12，got %v", rm["gdd"])
			}
			found = true
		}
	}
	if !found {
		t.Fatal("逐日结果中找不到 2026-03-02")
	}

	// 阶段查询。
	code, body = doJSON(t, h, "GET", "/api/v1/plots/P1/stages", nil)
	if code != 200 || body["data"] == nil {
		t.Fatalf("阶段查询失败：%d %v", code, body)
	}

	// 事件拉取。
	code, body = doJSON(t, h, "GET", "/api/v1/events?limit=10", nil)
	if code != 200 {
		t.Fatalf("事件查询失败：%d", code)
	}

	// 404。
	code, _ = doJSON(t, h, "GET", "/api/v1/plots/NOPE/stages", nil)
	if code != 404 {
		t.Fatalf("不存在地块应 404，got %d", code)
	}

	// 健康检查。
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("healthz 应 200，got %d", w.Code)
	}
}
