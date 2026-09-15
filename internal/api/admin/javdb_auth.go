package admin

import (
	"context"
	"net/http"

	"mediavault/internal/api"
	"mediavault/internal/db"

	"github.com/gin-gonic/gin"
)

// JavDBAuth is implemented by the application runtime and exposes JavDB account
// login/logout to the admin console.
type JavDBAuth interface {
	Login(ctx context.Context, username, password string) (string, error)
	Logout(ctx context.Context) error
	JavDBLoggedIn(ctx context.Context) bool
}

// JavDBHandler manages the optional JavDB account session.
type JavDBHandler struct {
	auth         JavDBAuth
	settingsRepo *db.SettingsRepo
}

func NewJavDBHandler(auth JavDBAuth, settingsRepo *db.SettingsRepo) *JavDBHandler {
	return &JavDBHandler{auth: auth, settingsRepo: settingsRepo}
}

type javdbLoginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// Login handles POST /api/v1/javdb/login.
func (h *JavDBHandler) Login(c *gin.Context) {
	if h.auth == nil {
		api.SendError(c, http.StatusServiceUnavailable, "not_available", "JavDB 登录功能未启用")
		return
	}
	var req javdbLoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "username and password are required")
		return
	}
	name, err := h.auth.Login(c.Request.Context(), req.Username, req.Password)
	if err != nil {
		api.SendError(c, http.StatusBadRequest, "login_failed", err.Error())
		return
	}
	api.SendSuccess(c, gin.H{"logged_in": true, "username": name})
}

// Logout handles POST /api/v1/javdb/logout.
func (h *JavDBHandler) Logout(c *gin.Context) {
	if h.auth == nil {
		api.SendError(c, http.StatusServiceUnavailable, "not_available", "JavDB 登录功能未启用")
		return
	}
	if err := h.auth.Logout(c.Request.Context()); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	api.SendSuccess(c, gin.H{"logged_in": false})
}

// Status handles GET /api/v1/javdb/status.
func (h *JavDBHandler) Status(c *gin.Context) {
	loggedIn := false
	if h.auth != nil {
		loggedIn = h.auth.JavDBLoggedIn(c.Request.Context())
	} else if h.settingsRepo != nil {
		if s, err := h.settingsRepo.GetSetting(c.Request.Context(), "javdb_token"); err == nil && s != nil && s.Value != nil {
			loggedIn = *s.Value != ""
		}
	}
	api.SendSuccess(c, gin.H{"logged_in": loggedIn})
}
