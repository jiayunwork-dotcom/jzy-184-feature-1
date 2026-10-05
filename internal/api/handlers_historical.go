package api

import (
	"net/http"
	"strconv"
	"strings"

	"agristation/internal/engine"
	"agristation/internal/model"

	"github.com/gin-gonic/gin"
)

type historicalReq struct {
	Records []engine.HistoricalInput `json:"records" binding:"required"`
}

// ingestHistorical 按站点和年份整年导入/替换历年逐日气温。
func (s *Server[T]) ingestHistorical(c *gin.Context) {
	var req historicalReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if len(req.Records) == 0 {
		fail(c, http.StatusBadRequest, errBad("records 不能为空"))
		return
	}
	res, err := s.svc.IngestHistorical(c.Request.Context(), req.Records)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": res})
}

// stageRanges 返回各阶段历年试走的范围与分位。
// quantiles 为逗号分隔分位，默认 0.1,0.5,0.9；date 为可选的“截至日”。
func (s *Server[T]) stageRanges(c *gin.Context) {
	var quantiles []float64
	if raw := c.Query("quantiles"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			q, err := strconv.ParseFloat(part, 64)
			if err != nil {
				fail(c, http.StatusBadRequest, errBad("分位必须是数字："+part))
				return
			}
			quantiles = append(quantiles, q)
		}
	}
	res, err := s.svc.StageRanges(c.Request.Context(), c.Param("code"),
		quantiles, c.Query("date"))
	if err != nil {
		status := http.StatusBadRequest
		if engine.AsNotFound(err) {
			status = http.StatusNotFound
		}
		fail(c, status, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": res})
}

// arrival 返回某阶段在某天或之前到达的年份比例。
func (s *Server[T]) arrival(c *gin.Context) {
	stage := model.Stage(c.Query("stage"))
	by := c.Query("by")
	if by == "" {
		fail(c, http.StatusBadRequest, errBad("必须提供 by（YYYY-MM-DD）"))
		return
	}
	res, err := s.svc.ArrivalBy(c.Request.Context(), c.Param("code"),
		stage, by, c.Query("date"))
	if err != nil {
		status := http.StatusBadRequest
		if engine.AsNotFound(err) {
			status = http.StatusNotFound
		}
		fail(c, status, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": res})
}
