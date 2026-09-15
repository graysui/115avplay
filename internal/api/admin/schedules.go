package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"mediavault/internal/api"
	"mediavault/internal/db"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
)

type SchedulesHandler struct {
	settingsRepo *db.SettingsRepo
}

func NewSchedulesHandler(settingsRepo *db.SettingsRepo) *SchedulesHandler {
	return &SchedulesHandler{settingsRepo: settingsRepo}
}

// ListSchedules handles GET /api/v1/schedules.
func (h *SchedulesHandler) ListSchedules(c *gin.Context) {
	schedules, err := h.settingsRepo.ListSchedules(c.Request.Context())
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to list schedules: "+err.Error())
		return
	}
	if schedules == nil {
		schedules = []models.Schedule{}
	}
	api.SendSuccess(c, schedules)
}

type scheduleReq struct {
	ID         string                 `json:"id" binding:"required"`
	Kind       string                 `json:"kind" binding:"required"` // sync30d, rankings, scan
	Timezone   string                 `json:"timezone"`
	RuleJSON   string                 `json:"rule_json" binding:"required"`
	ParamsJSON map[string]interface{} `json:"params"`
	Enabled    int                    `json:"enabled"`
}

// CreateSchedule handles POST /api/v1/schedules.
func (h *SchedulesHandler) CreateSchedule(c *gin.Context) {
	var req scheduleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "id, kind, and rule_json are required")
		return
	}

	if err := validateSchedule(req); err != nil {
		api.SendError(c, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	tz := req.Timezone
	if tz == "" {
		tz = "Asia/Shanghai"
	}

	paramsStr := "{}"
	if req.ParamsJSON != nil {
		b, _ := json.Marshal(req.ParamsJSON)
		paramsStr = string(b)
	}

	s := &models.Schedule{
		ID:         req.ID,
		Kind:       req.Kind,
		Timezone:   tz,
		RuleJSON:   req.RuleJSON,
		ParamsJSON: paramsStr,
		Enabled:    req.Enabled,
	}

	if err := h.settingsRepo.UpsertSchedule(c.Request.Context(), s); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to save schedule: "+err.Error())
		return
	}

	api.SendSuccess(c, s)
}

// UpdateSchedule handles PUT /api/v1/schedules/:id.
func (h *SchedulesHandler) UpdateSchedule(c *gin.Context) {
	id := c.Param("id")
	var req scheduleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "invalid request payload")
		return
	}
	req.ID = id

	if err := validateSchedule(req); err != nil {
		api.SendError(c, http.StatusBadRequest, "validation_failed", err.Error())
		return
	}

	tz := req.Timezone
	if tz == "" {
		tz = "Asia/Shanghai"
	}

	paramsStr := "{}"
	if req.ParamsJSON != nil {
		b, _ := json.Marshal(req.ParamsJSON)
		paramsStr = string(b)
	}

	s := &models.Schedule{
		ID:         id,
		Kind:       req.Kind,
		Timezone:   tz,
		RuleJSON:   req.RuleJSON,
		ParamsJSON: paramsStr,
		Enabled:    req.Enabled,
	}

	if err := h.settingsRepo.UpsertSchedule(c.Request.Context(), s); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to update schedule: "+err.Error())
		return
	}

	api.SendSuccess(c, s)
}

// DeleteSchedule handles DELETE /api/v1/schedules/:id.
func (h *SchedulesHandler) DeleteSchedule(c *gin.Context) {
	id := c.Param("id")
	if err := h.settingsRepo.DeleteSchedule(c.Request.Context(), id); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to delete schedule: "+err.Error())
		return
	}
	api.SendSuccess(c, gin.H{"deleted": true, "id": id})
}

func validateSchedule(req scheduleReq) error {
	// 1. Kind check
	validKinds := map[string]bool{
		"sync30d":  true,
		"rankings": true,
		"scan":     true,
	}
	if !validKinds[req.Kind] {
		return errors.New("invalid kind: must be sync30d, rankings, or scan")
	}

	// 2. Timezone check
	tz := req.Timezone
	if tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return errors.New("invalid IANA timezone: " + tz)
		}
	}

	// 3. Rule JSON validation (must be valid JSON and have type daily or weekly)
	var rule map[string]interface{}
	if err := json.Unmarshal([]byte(req.RuleJSON), &rule); err != nil {
		return errors.New("rule_json must be valid JSON")
	}

	ruleType, _ := rule["type"].(string)
	if ruleType != "daily" && ruleType != "weekly" && ruleType != "interval" {
		return errors.New("rule_json type must be daily, weekly, or interval")
	}

	return nil
}
