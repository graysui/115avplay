package admin

import (
	"encoding/json"
	"net/http"

	"mediavault/internal/api"
	"mediavault/internal/client115"
	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
)

type Cloud115Handler struct {
	assetRepo    *db.AssetRepo
	settingsRepo *db.SettingsRepo
	appConfig    *config.AppConfig
	authClient   *client115.AuthClient
}

func NewCloud115Handler(
	assetRepo *db.AssetRepo,
	settingsRepo *db.SettingsRepo,
	appConfig *config.AppConfig,
	authClient *client115.AuthClient,
) *Cloud115Handler {
	return &Cloud115Handler{
		assetRepo:    assetRepo,
		settingsRepo: settingsRepo,
		appConfig:    appConfig,
		authClient:   authClient,
	}
}

// GetStatus handles GET /api/v1/115/status.
func (h *Cloud115Handler) GetStatus(c *gin.Context) {
	binding, err := h.assetRepo.GetActiveBinding(c.Request.Context(), "115")
	if err != nil || binding == nil {
		api.SendSuccess(c, gin.H{
			"configured": false,
			"provider":   "115",
		})
		return
	}

	api.SendSuccess(c, gin.H{
		"configured":       true,
		"provider":         "115",
		"provider_user_id": binding.ProviderUserID,
		"binding_id":       binding.ID,
		"enabled":          binding.Enabled == 1,
		"updated_at":       binding.UpdatedAt,
	})
}

// StartDeviceAuth handles POST /api/v1/115/auth/device.
func (h *Cloud115Handler) StartDeviceAuth(c *gin.Context) {
	if h.authClient == nil {
		api.SendError(c, http.StatusServiceUnavailable, "auth_client_unavailable", "115 auth client not configured")
		return
	}

	resp, err := h.authClient.StartDeviceAuth(c.Request.Context())
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "device_auth_failed", err.Error())
		return
	}

	api.SendSuccess(c, resp)
}

type pollDeviceTokenReq struct {
	DeviceCode   string `json:"device_code" binding:"required"`
	CodeVerifier string `json:"code_verifier" binding:"required"`
}

// PollDeviceToken handles POST /api/v1/115/auth/poll.
func (h *Cloud115Handler) PollDeviceToken(c *gin.Context) {
	if h.authClient == nil {
		api.SendError(c, http.StatusServiceUnavailable, "auth_client_unavailable", "115 auth client not configured")
		return
	}

	var req pollDeviceTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "device_code and code_verifier are required")
		return
	}

	ctx := c.Request.Context()
	tokenData, err := h.authClient.PollDeviceToken(ctx, req.DeviceCode, req.CodeVerifier)
	if err != nil {
		api.SendError(c, http.StatusBadRequest, "poll_failed", err.Error())
		return
	}

	// Persist binding and encrypted tokens
	bindingID := "bind_115_" + tokenData.UserID
	secretKey := "provider_tokens:" + bindingID

	tokenJSON, _ := json.Marshal(tokenData)
	secretEnvelopes := make(map[string]string)

	if h.appConfig != nil && h.appConfig.MasterKey != nil {
		envJSON, err := h.appConfig.MasterKey.Encrypt(secretKey, tokenJSON)
		if err == nil {
			secretEnvelopes[secretKey] = envJSON
		}
	}

	// Save encrypted token to settings
	_, _ = h.settingsRepo.UpdateSettings(ctx, 0, nil, secretEnvelopes, nil)

	// Upsert binding in cloud_bindings
	now := models.UTCNow()
	binding := &models.CloudBinding{
		ID:               bindingID,
		Provider:         "115",
		ProviderUserID:   tokenData.UserID,
		Enabled:          1,
		SecretSettingKey: secretKey,
		ConfigRevision:   1,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := h.assetRepo.UpsertBinding(ctx, binding); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to save cloud binding: "+err.Error())
		return
	}

	api.SendSuccess(c, gin.H{
		"bound":            true,
		"provider_user_id": tokenData.UserID,
		"binding_id":       bindingID,
	})
}
