package api

import (
	"net/http"
	"strconv"

	"agristation/internal/engine"

	"github.com/gin-gonic/gin"
)

type ingestReq struct {
	Records []engine.ObservationInput `json:"records" binding:"required"`
}

func (s *Server[T]) ingestObservations(c *gin.Context) {
	var req ingestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if len(req.Records) == 0 {
		fail(c, http.StatusBadRequest, errBad("records 不能为空"))
		return
	}
	res, err := s.svc.IngestObservations(c.Request.Context(), req.Records)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": res})
}

func (s *Server[T]) plotDaily(c *gin.Context) {
	rows, err := s.svc.PlotDaily(c.Request.Context(),
		c.Param("code"),
		c.Query("from"), c.Query("to"), c.Query("date"))
	if err != nil {
		status := http.StatusInternalServerError
		if engine.AsNotFound(err) {
			status = http.StatusNotFound
		}
		fail(c, status, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": rows})
}

func (s *Server[T]) plotStages(c *gin.Context) {
	rows, err := s.svc.PlotStages(c.Request.Context(), c.Param("code"), c.Query("date"))
	if err != nil {
		status := http.StatusInternalServerError
		if engine.AsNotFound(err) {
			status = http.StatusNotFound
		}
		fail(c, status, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": rows})
}

func (s *Server[T]) listEvents(c *gin.Context) {
	var after int64
	if v := c.Query("after_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			fail(c, http.StatusBadRequest, errBad("after_id 必须是整数"))
			return
		}
		after = n
	}
	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	events, err := s.svc.ListEvents(c.Request.Context(), after, limit, c.Query("plot"))
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	var maxID int64
	for _, e := range events {
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": events, "next_after_id": maxID})
}
