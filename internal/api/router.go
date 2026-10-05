// Package api 是 Gin HTTP 接口层：参数解析与校验、调用 engine、统一响应格式。
package api

import (
	"net/http"

	"agristation/internal/engine"

	"github.com/gin-gonic/gin"
)

// Server 持有业务引擎，T 为事务句柄类型。
type Server[T engine.Tx] struct {
	svc *engine.Service[T]
}

// NewServer 构造 API Server。
func NewServer[T engine.Tx](svc *engine.Service[T]) *Server[T] {
	return &Server[T]{svc: svc}
}

// Router 组装全部路由。
func (s *Server[T]) Router() *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	v1 := r.Group("/api/v1")
	{
		// 档案登记与修改
		v1.POST("/stations", s.registerStation)
		v1.GET("/stations", s.listStations)
		v1.POST("/varieties", s.registerVariety)
		v1.POST("/plots", s.registerPlot)
		v1.GET("/plots", s.listPlots)
		v1.POST("/plots/:code/bindings", s.bindPlot)
		v1.GET("/plots/:code/bindings", s.listBindings)
		v1.POST("/climate-normals", s.putClimateNormal)

		// 观测批量写入
		v1.POST("/observations/batch", s.ingestObservations)

		// 历年资料导入（一站一年整体生效/替换）
		v1.POST("/historical/batch", s.importHistorical)

		// 历年试走范围与“某天之前达到的把握”
		v1.GET("/plots/:code/outlook", s.plotOutlook)
		v1.GET("/plots/:code/stage-probability", s.stageProbability)

		// 查询
		v1.GET("/plots/:code/daily", s.plotDaily)
		v1.GET("/plots/:code/stages", s.plotStages)

		// 变更事件
		v1.GET("/events", s.listEvents)
	}
	return r
}

func fail(c *gin.Context, status int, err error) {
	c.JSON(status, gin.H{"ok": false, "error": err.Error()})
}
