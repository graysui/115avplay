package admin

import (
	"errors"
	"fmt"
	"net/http"

	"mediavault/internal/api"
	"mediavault/internal/config"
	"mediavault/internal/db"

	"github.com/gin-gonic/gin"
)

type SettingsHandler struct {
	settingsRepo *db.SettingsRepo
	appConfig    *config.AppConfig
}

func NewSettingsHandler(settingsRepo *db.SettingsRepo, appConfig *config.AppConfig) *SettingsHandler {
	return &SettingsHandler{
		settingsRepo: settingsRepo,
		appConfig:    appConfig,
	}
}

type secretActionReq struct {
	Action string `json:"action"` // keep, replace, clear
	Value  string `json:"value,omitempty"`
}

type updateSettingsReq struct {
	Revision int                        `json:"revision"`
	Values   map[string]string          `json:"values"`
	Secrets  map[string]secretActionReq `json:"secrets"`
}

// GetSettings handles GET /api/v1/settings.
func (h *SettingsHandler) GetSettings(c *gin.Context) {
	ctx := c.Request.Context()
	rawSettings, revision, err := h.settingsRepo.GetAllSettings(ctx)
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to read settings")
		return
	}

	values := make(map[string]string)
	secrets := make(map[string]gin.H)

	// List of known secrets
	knownSecrets := map[string]bool{
		"proxy_url":     true,
		"alert_webhook": true,
		"javdb_token":   true,
		"javdb_cookie":  true,
		"115_cookie":    true,
	}

	for k, s := range rawSettings {
		if s.IsSecret == 1 || knownSecrets[k] {
			isConfigured := s.Value != nil && *s.Value != ""
			secrets[k] = gin.H{
				"configured": isConfigured,
			}
		} else {
			if s.Value != nil {
				values[k] = *s.Value
			} else {
				values[k] = ""
			}
		}
	}

	// For known secrets that aren't yet in DB, report configured=false
	for sKey := range knownSecrets {
		if _, ok := secrets[sKey]; !ok {
			secrets[sKey] = gin.H{"configured": false}
		}
	}

	// Mark env overrides
	envOverrides := make(map[string]string)
	if h.appConfig != nil && h.appConfig.EnvOverrides != nil {
		for k, v := range h.appConfig.EnvOverrides {
			envOverrides[k] = v
			// Environment override takes precedence in returned values
			values[k] = v
		}
	}

	api.SendSuccess(c, gin.H{
		"revision":      revision,
		"values":        values,
		"secrets":       secrets,
		"env_overrides": envOverrides,
	})
}

// UpdateSettings handles PUT /api/v1/settings.
func (h *SettingsHandler) UpdateSettings(c *gin.Context) {
	var req updateSettingsReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "invalid request format")
		return
	}

	ctx := c.Request.Context()

	// 1. Check if any modified key is overridden by environment
	if h.appConfig != nil && h.appConfig.EnvOverrides != nil {
		for k := range req.Values {
			if _, exists := h.appConfig.EnvOverrides[k]; exists {
				api.SendError(c, http.StatusBadRequest, "env_override_readonly", fmt.Sprintf("setting %s is overridden by environment and cannot be modified via API", k))
				return
			}
		}
		for k := range req.Secrets {
			if _, exists := h.appConfig.EnvOverrides[k]; exists {
				api.SendError(c, http.StatusBadRequest, "env_override_readonly", fmt.Sprintf("secret %s is overridden by environment and cannot be modified via API", k))
				return
			}
		}
	}

	// 2. Validate all normal values
	for k, v := range req.Values {
		if err := config.ValidateSetting(k, v); err != nil {
			api.SendError(c, http.StatusBadRequest, "validation_failed", fmt.Sprintf("setting %s: %v", k, err))
			return
		}
	}

	// 3. Process secret actions
	secretEnvelopes := make(map[string]string)
	var secretsToClear []string

	for k, action := range req.Secrets {
		switch action.Action {
		case "keep":
			// Keep existing secret as is (no modification)
			continue
		case "clear":
			secretsToClear = append(secretsToClear, k)
		case "replace":
			if h.appConfig == nil || h.appConfig.MasterKey == nil {
				api.SendError(c, http.StatusInternalServerError, "master_key_missing", "master key is not initialized")
				return
			}
			envelopeJSON, err := h.appConfig.MasterKey.Encrypt(k, []byte(action.Value))
			if err != nil {
				api.SendError(c, http.StatusInternalServerError, "encryption_failed", "failed to encrypt secret")
				return
			}
			secretEnvelopes[k] = envelopeJSON
		default:
			api.SendError(c, http.StatusBadRequest, "invalid_secret_action", fmt.Sprintf("invalid secret action for %s: must be keep, replace, or clear", k))
			return
		}
	}

	// 4. Atomic update with optimistic lock check
	newRev, err := h.settingsRepo.UpdateSettings(ctx, req.Revision, req.Values, secretEnvelopes, secretsToClear)
	if err != nil {
		if errors.Is(err, db.ErrRevisionMismatch) {
			api.SendError(c, http.StatusConflict, "revision_conflict", "configuration has been modified by another process; please refresh and retry")
			return
		}
		api.SendError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	api.SendSuccess(c, gin.H{
		"revision": newRev,
		"updated":  true,
	})
}
