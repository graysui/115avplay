package admin

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"mediavault/internal/api"
	"mediavault/internal/db"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type AuthHandler struct {
	userRepo *db.UserRepo
}

func NewAuthHandler(userRepo *db.UserRepo) *AuthHandler {
	return &AuthHandler{userRepo: userRepo}
}

type adminLoginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type adminLoginResp struct {
	Token              string          `json:"token"`
	CSRFToken          string          `json:"csrf_token"`
	MustChangePassword bool            `json:"must_change_password"`
	User               models.User     `json:"user"`
}

// Login handles POST /api/v1/auth/login.
func (h *AuthHandler) Login(c *gin.Context) {
	var req adminLoginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "username and password are required")
		return
	}

	user, err := h.userRepo.GetUserByUsername(c.Request.Context(), req.Username)
	if err != nil || user == nil {
		api.SendError(c, http.StatusUnauthorized, "invalid_credentials", "incorrect username or password")
		return
	}

	valid, err := db.VerifyPassword(user.PasswordHash, req.Password)
	if err != nil || !valid {
		api.SendError(c, http.StatusUnauthorized, "invalid_credentials", "incorrect username or password")
		return
	}

	if user.IsAdmin != 1 {
		api.SendError(c, http.StatusForbidden, "forbidden", "user does not have administrator privileges")
		return
	}

	if user.Enabled != 1 {
		api.SendError(c, http.StatusForbidden, "user_disabled", "user account is disabled")
		return
	}

	// Generate secure token (32 bytes hex)
	tokenBytes := make([]byte, 32)
	_, _ = rand.Read(tokenBytes)
	plainToken := hex.EncodeToString(tokenBytes)

	// Generate CSRF token (16 bytes hex)
	csrfBytes := make([]byte, 16)
	_, _ = rand.Read(csrfBytes)
	csrfToken := hex.EncodeToString(csrfBytes)

	hasher := sha256.New()
	hasher.Write([]byte(plainToken))
	tokenHash := hex.EncodeToString(hasher.Sum(nil))

	now := models.UTCNow()
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	sessionID := "sess_admin_" + uuid.New().String()[:8]

	deviceName := "Admin Console"
	clientName := c.Request.UserAgent()
	session := &models.AuthSession{
		ID:         sessionID,
		UserID:     user.ID,
		TokenHash:  tokenHash,
		Audience:   "admin",
		DeviceID:   "admin_browser",
		DeviceName: &deviceName,
		ClientName: &clientName,
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
	}

	if err := h.userRepo.CreateSession(c.Request.Context(), session); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to create session")
		return
	}

	// Set HttpOnly, SameSite=Lax Cookie
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("mv_session", plainToken, 86400, "/", "", false, true)
	c.SetCookie("mv_csrf", csrfToken, 86400, "/", "", false, false)

	api.SendSuccess(c, adminLoginResp{
		Token:              plainToken,
		CSRFToken:          csrfToken,
		MustChangePassword: user.MustChangePassword == 1,
		User: models.User{
			ID:                 user.ID,
			Username:           user.Username,
			IsAdmin:            user.IsAdmin,
			Enabled:            user.Enabled,
			MustChangePassword: user.MustChangePassword,
			CreatedAt:          user.CreatedAt,
			UpdatedAt:          user.UpdatedAt,
		},
	})
}

// Logout handles POST /api/v1/auth/logout.
func (h *AuthHandler) Logout(c *gin.Context) {
	sessionVal, exists := c.Get("admin_session")
	if exists {
		if sess, ok := sessionVal.(*models.AuthSession); ok {
			_ = h.userRepo.DeleteSession(c.Request.Context(), sess.ID)
		}
	}

	// Clear cookies
	c.SetCookie("mv_session", "", -1, "/", "", false, true)
	c.SetCookie("mv_csrf", "", -1, "/", "", false, false)

	api.SendSuccess(c, gin.H{"logged_out": true})
}

// Me handles GET /api/v1/auth/me.
func (h *AuthHandler) Me(c *gin.Context) {
	userVal, exists := c.Get("admin_user")
	if !exists {
		api.SendError(c, http.StatusUnauthorized, "unauthorized", "not authenticated")
		return
	}
	user := userVal.(*models.User)
	api.SendSuccess(c, gin.H{
		"id":                   user.ID,
		"username":             user.Username,
		"is_admin":             user.IsAdmin == 1,
		"must_change_password": user.MustChangePassword == 1,
	})
}

type passwordChangeReq struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}

// ChangePassword handles POST /api/v1/auth/password.
func (h *AuthHandler) ChangePassword(c *gin.Context) {
	var req passwordChangeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "old_password and new_password are required")
		return
	}

	if len(req.NewPassword) < 6 {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "new password must be at least 6 characters")
		return
	}

	userVal, _ := c.Get("admin_user")
	user := userVal.(*models.User)

	// Verify old password
	valid, err := db.VerifyPassword(user.PasswordHash, req.OldPassword)
	if err != nil || !valid {
		api.SendError(c, http.StatusBadRequest, "invalid_old_password", "incorrect current password")
		return
	}

	// Update password
	if err := h.userRepo.UpdatePassword(c.Request.Context(), user.ID, req.NewPassword); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to update password")
		return
	}

	// Revoke other sessions
	_ = h.userRepo.RevokeAllUserSessions(c.Request.Context(), user.ID)

	api.SendSuccess(c, gin.H{"password_updated": true})
}

// AdminAuthMiddleware validates admin session and prevents ordinary Emby tokens.
func AdminAuthMiddleware(userRepo *db.UserRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := extractAdminToken(c.Request)
		if token == "" {
			api.SendError(c, http.StatusUnauthorized, "unauthorized", "missing administrator session token")
			c.Abort()
			return
		}

		hasher := sha256.New()
		hasher.Write([]byte(token))
		tokenHash := hex.EncodeToString(hasher.Sum(nil))

		now := models.UTCNow()
		// Must match audience='admin' specifically!
		session, user, err := userRepo.GetSessionByTokenHash(c.Request.Context(), tokenHash, "admin", now)
		if err != nil || user == nil || user.IsAdmin != 1 || user.Enabled != 1 {
			api.SendError(c, http.StatusUnauthorized, "unauthorized", "invalid or expired administrator session")
			c.Abort()
			return
		}

		// CSRF verification for state-modifying methods if session came from cookie
		if c.Request.Method == "POST" || c.Request.Method == "PUT" || c.Request.Method == "DELETE" || c.Request.Method == "PATCH" {
			if cookieToken, err := c.Cookie("mv_session"); err == nil && cookieToken != "" {
				csrfHeader := c.GetHeader("X-CSRF-Token")
				csrfCookie, _ := c.Cookie("mv_csrf")
				if csrfHeader == "" || csrfHeader != csrfCookie {
					api.SendError(c, http.StatusForbidden, "csrf_mismatch", "invalid CSRF token")
					c.Abort()
					return
				}
			}
		}

		c.Set("admin_user", user)
		c.Set("admin_session", session)
		c.Next()
	}
}

func extractAdminToken(r *http.Request) string {
	// 1. Authorization: Bearer <token>
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}

	// 2. Cookie mv_session
	if cookie, err := r.Cookie("mv_session"); err == nil && cookie.Value != "" {
		return strings.TrimSpace(cookie.Value)
	}

	// 3. Query token
	if q := r.URL.Query().Get("token"); q != "" {
		return strings.TrimSpace(q)
	}

	return ""
}
