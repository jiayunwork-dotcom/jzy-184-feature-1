package engine

import (
	"fmt"

	"agristation/internal/gdd"
	"agristation/internal/model"
)

// 温度合理范围（℃）。覆盖我国县域气象站可能出现的气温。
const (
	MinTemp = -60.0
	MaxTemp = 65.0
)

// ValidateTemp 校验一对最高最低气温。
func ValidateTemp(tmax, tmin float64) error {
	if tmin > tmax {
		return fmt.Errorf("最低气温 %.2f 高于最高气温 %.2f", tmin, tmax)
	}
	if tmin < MinTemp || tmin > MaxTemp || tmax < MinTemp || tmax > MaxTemp {
		return fmt.Errorf("温度超出合理范围 [%g, %g] ℃", MinTemp, MaxTemp)
	}
	return nil
}

// ValidateVariety 校验品种参数。
func ValidateVariety(v *model.Variety) error {
	if v.Code == "" {
		return fmt.Errorf("品种代码不能为空")
	}
	if v.BaseTemp >= v.UpperTemp {
		return fmt.Errorf("基点温度 %.2f 不低于上限温度 %.2f", v.BaseTemp, v.UpperTemp)
	}
	if len(v.Thresholds) != len(model.StageOrder) {
		return fmt.Errorf("阶段积温需求必须是 %d 个（出苗、拔节、抽雄、吐丝、成熟）", len(model.StageOrder))
	}
	for i := 1; i < len(v.Thresholds); i++ {
		if v.Thresholds[i] <= v.Thresholds[i-1] {
			return fmt.Errorf("阶段积温需求必须严格递增：第 %d 个 %.2f 不大于前一个 %.2f",
				i+1, v.Thresholds[i], v.Thresholds[i-1])
		}
	}
	if v.Thresholds[0] <= 0 {
		return fmt.Errorf("出苗积温需求必须为正")
	}
	if _, err := gdd.ParseMethod(""); err != nil {
		return err
	}
	return nil
}

// ValidateMethod 校验地块口径。
func ValidateMethod(m string) error {
	_, err := gdd.ParseMethod(m)
	return err
}

// ValidateSowQuery 播种日期不得晚于查询日期。
func ValidateSowQuery(sow, query model.Date) error {
	if sow.After(query) {
		return fmt.Errorf("播种日期 %s 晚于查询日期 %s", sow.Format("2006-01-02"), query.Format("2006-01-02"))
	}
	return nil
}
