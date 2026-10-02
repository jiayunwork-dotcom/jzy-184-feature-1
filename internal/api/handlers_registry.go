package api

import (
	"net/http"
	"time"

	"agristation/internal/engine"
	"agristation/internal/model"

	"github.com/gin-gonic/gin"
)

type stationReq struct {
	Code      string  `json:"code"`
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Elevation float64 `json:"elevation"`
}

func (s *Server[T]) registerStation(c *gin.Context) {
	var req stationReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	st := model.Station{
		Code: req.Code, Name: req.Name,
		Latitude: req.Latitude, Longitude: req.Longitude, Elevation: req.Elevation,
	}
	if err := s.svc.RegisterStation(c.Request.Context(), st); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server[T]) listStations(c *gin.Context) {
	sts, err := s.svc.ListStations(c.Request.Context())
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": sts})
}

type varietyReq struct {
	Code       string    `json:"code"`
	Name       string    `json:"name"`
	BaseTemp   float64   `json:"base_temp"`
	UpperTemp  float64   `json:"upper_temp"`
	Thresholds []float64 `json:"thresholds"`
}

func (s *Server[T]) registerVariety(c *gin.Context) {
	var req varietyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	v := model.Variety{
		Code: req.Code, Name: req.Name,
		BaseTemp: req.BaseTemp, UpperTemp: req.UpperTemp, Thresholds: req.Thresholds,
	}
	if err := s.svc.RegisterVariety(c.Request.Context(), v); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type plotReq struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	SowDate string `json:"sow_date"`
	Variety string `json:"variety"`
	Method  string `json:"method"`
	// Station 为登记时的初始绑定站（首次登记必填）。
	Station string `json:"station"`
}

func parseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}

func (s *Server[T]) registerPlot(c *gin.Context) {
	var req plotReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if req.Station == "" {
		fail(c, http.StatusBadRequest, errBad("登记地块必须指定初始绑定站点"))
		return
	}
	sow, err := parseDate(req.SowDate)
	if err != nil {
		fail(c, http.StatusBadRequest, errBad("播种日期格式应为 YYYY-MM-DD："+req.SowDate))
		return
	}
	sow = sow.UTC()
	p := model.Plot{
		Code: req.Code, Name: req.Name, SowDate: sow,
		Variety: req.Variety, Method: req.Method,
	}
	saved, err := s.svc.RegisterPlot(c.Request.Context(), p, req.Station)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": saved})
}

func (s *Server[T]) listPlots(c *gin.Context) {
	plots, err := s.svc.ListPlots(c.Request.Context())
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": plots})
}

type bindReq struct {
	StationCode   string `json:"station_code"`
	EffectiveDate string `json:"effective_date"`
}

func (s *Server[T]) bindPlot(c *gin.Context) {
	var req bindReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	err := s.svc.BindPlot(c.Request.Context(), engine.BindPlotInput{
		PlotCode:      c.Param("code"),
		StationCode:   req.StationCode,
		EffectiveDate: req.EffectiveDate,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (s *Server[T]) listBindings(c *gin.Context) {
	out, err := s.svc.ListBindings(c.Request.Context(), c.Param("code"))
	if err != nil {
		status := http.StatusInternalServerError
		if engine.AsNotFound(err) {
			status = http.StatusNotFound
		}
		fail(c, status, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "data": out})
}

type climateReq struct {
	StationCode string  `json:"station_code"`
	DOY         int     `json:"doy"`
	TMax        float64 `json:"tmax"`
	TMin        float64 `json:"tmin"`
}

func (s *Server[T]) putClimateNormal(c *gin.Context) {
	var req climateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	err := s.svc.PutClimateNormal(c.Request.Context(), model.ClimateNormal{
		StationCode: req.StationCode, DOY: req.DOY, TMax: req.TMax, TMin: req.TMin,
	})
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

type simpleError string

func (e simpleError) Error() string { return string(e) }
func errBad(msg string) error       { return simpleError(msg) }
