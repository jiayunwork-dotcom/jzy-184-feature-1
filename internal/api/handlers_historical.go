package api

import (
	"net/http"
	"strconv"
	"strings"

	"agristation/internal/engine"

	"github.com/gin-gonic/gin"
)

type historicalReq struct {
	Records []engine.HistoricalInput `json:"records" binding:"required"`
}

// importHistorical 按站-年导入整年逐日气温；逐条回报，一站一年整体生效。
func (s *Server[T]) importHistorical(c *gin.Context) {
	var req historicalReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if len(req.Records) == 0 {
		fail(c, http.StatusBadRequest, errBad("records 不能为空"))
		return
	}
	res, err := s.svc.ImportHistorical(c.Request.Context(), req.Records)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": res})
}

// plotOutlook 五个阶段的历年试走范围。
// 可选参数：date=asOf（YYYY-MM-DD），quantiles=0.1,0.5,0.9。
func (s *Server[T]) plotOutlook(c *gin.Context) {
	qs, err := parseQuantiles(c.Query("quantiles"))
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	out, err := s.svc.PlotOutlook(c.Request.Context(), c.Param("code"), c.Query("date"), qs)
	if err != nil {
		writeOutlookError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
}

// stageProbability “该阶段在某天或之前达到的比例”。
// 参数：stage（必填）、by（必填 YYYY-MM-DD）、date（可选 asOf）。
func (s *Server[T]) stageProbability(c *gin.Context) {
	res, err := s.svc.PlotStageProbability(c.Request.Context(), engine.StageProbabilityQuery{
		PlotCode:  c.Param("code"),
		Stage:     c.Query("stage"),
		By:        c.Query("by"),
		QueryDate: c.Query("date"),
	})
	if err != nil {
		writeOutlookError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": res})
}

func writeOutlookError(c *gin.Context, err error) {
	switch {
	case engine.AsNotFound(err):
		fail(c, http.StatusNotFound, err)
	case engine.AsNoHistory(err):
		// 明确告诉调用方给不出范围，而不是回一个看着正常的空结果。
		fail(c, http.StatusUnprocessableEntity, err)
	default:
		fail(c, http.StatusBadRequest, err)
	}
}

// parseQuantiles 解析逗号分隔的分位列表；空串给 nil（走默认）。
func parseQuantiles(raw string) ([]float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]float64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return nil, errBad("分位必须是 0 到 1 之间的数：" + p)
		}
		if v < 0 || v > 1 || v != v {
			return nil, errBad("分位必须在 0 到 1 之间：" + p)
		}
		out = append(out, v)
	}
	return out, nil
}
