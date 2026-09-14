package admin

import (
	"net/http"

	"mediavault/internal/api"
	"mediavault/internal/db"

	"github.com/gin-gonic/gin"
)

type AlertsHandler struct {
	settingsRepo *db.SettingsRepo
}

func NewAlertsHandler(settingsRepo *db.SettingsRepo) *AlertsHandler {
	return &AlertsHandler{settingsRepo: settingsRepo}
}

// ListAlerts handles GET /api/v1/alerts.
func (h *AlertsHandler) ListAlerts(c *gin.Context) {
	activeOnly := c.Query("active_only") == "true" || c.Query("active_only") == "1"
	alerts, err := h.settingsRepo.ListAlerts(c.Request.Context(), activeOnly)
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to list alerts: "+err.Error())
		return
	}
	api.SendSuccess(c, alerts)
}

// ResolveAlert handles POST /api/v1/alerts/:key/resolve.
func (h *AlertsHandler) ResolveAlert(c *gin.Context) {
	key := c.Param("key")
	if err := h.settingsRepo.ResolveAlert(c.Request.Context(), key); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to resolve alert: "+err.Error())
		return
	}
	api.SendSuccess(c, gin.H{"resolved": true, "key": key})
}
