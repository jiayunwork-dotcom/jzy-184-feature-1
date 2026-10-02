package fill

import (
	"math"
	"testing"

	"agristation/internal/model"
)

func station(code string, lat, lon, elev float64) model.Station {
	return model.Station{Code: code, Latitude: lat, Longitude: lon, Elevation: elev}
}

// TestNearestNeighbor 选择距离最近、海拔差合理的站，并做海拔直减修正。
func TestNearestNeighbor(t *testing.T) {
	target := station("S0", 30.0, 100.0, 500)
	near := station("S1", 30.01, 100.01, 600) // 约 1.4km，高 100m
	far := station("S2", 30.5, 100.5, 500)    // 约 73km
	cands := []Candidate{
		{Station: far, TMax: 33, TMin: 22},
		{Station: near, TMax: 30, TMin: 20},
	}
	r := Missing(target, cands, nil)
	if r == nil || r.Method != model.FillNeighbor {
		t.Fatalf("应选邻站补值：%+v", r)
	}
	if *r.FromStation != "S1" {
		t.Fatalf("应选最近的 S1，got %v", r.FromStation)
	}
	// 捐赠站高 100m：目标处更暖，T += 0.0065*100 = 0.65。
	if math.Abs(r.TMax-30.65) > 1e-9 || math.Abs(r.TMin-20.65) > 1e-9 {
		t.Fatalf("海拔修正错误：tmax=%g tmin=%g", r.TMax, r.TMin)
	}
}

// TestElevationFilter 海拔差过大的站不参与。
func TestElevationFilter(t *testing.T) {
	target := station("S0", 30.0, 100.0, 500)
	high := station("S1", 30.01, 100.01, 2000) // 高差 1500m，排除
	n := model.ClimateNormal{StationCode: "S0", DOY: 1, TMax: 28, TMin: 18}
	r := Missing(target, []Candidate{{Station: high, TMax: 20, TMin: 5}}, &n)
	if r == nil || r.Method != model.FillNormal {
		t.Fatalf("高海拔邻站应被排除并回退气候平均：%+v", r)
	}
	if r.TMax != 28 || r.TMin != 18 {
		t.Fatalf("气候平均取值错误：%+v", r)
	}
}

// TestNormalFallbackAndNone 无邻站用气候平均；都没有返回 nil（不编造）。
func TestNormalFallbackAndNone(t *testing.T) {
	target := station("S0", 30.0, 100.0, 500)
	if r := Missing(target, nil, nil); r != nil {
		t.Fatalf("无任何来源应返回 nil，got %+v", r)
	}
	n := model.ClimateNormal{TMax: 26, TMin: 16}
	r := Missing(target, nil, &n)
	if r == nil || r.Method != model.FillNormal || r.TMax != 26 {
		t.Fatalf("应回退气候平均：%+v", r)
	}
}

// TestTooFarIgnored 超出距离上限的邻站不采用。
func TestTooFarIgnored(t *testing.T) {
	target := station("S0", 30.0, 100.0, 500)
	far := station("S1", 31.0, 100.0, 500) // 约 111km，超 80km
	if d := Haversine(30, 100, 31, 100); d < 100000 {
		t.Fatalf("距离计算异常：%g", d)
	}
	r := Missing(target, []Candidate{{Station: far, TMax: 30, TMin: 20}}, nil)
	if r != nil {
		t.Fatalf("过远邻站应被忽略：%+v", r)
	}
}
