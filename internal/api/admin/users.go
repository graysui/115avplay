package admin

import (
	"net/http"

	"mediavault/internal/api"
	"mediavault/internal/db"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type UsersHandler struct {
	userRepo *db.UserRepo
}

func NewUsersHandler(userRepo *db.UserRepo) *UsersHandler {
	return &UsersHandler{userRepo: userRepo}
}

type safeUserResp struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	IsAdmin            int    `json:"is_admin"`
	Enabled            int    `json:"enabled"`
	MustChangePassword int    `json:"must_change_password"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

func toSafeUser(u models.User) safeUserResp {
	return safeUserResp{
		ID:                 u.ID,
		Username:           u.Username,
		IsAdmin:            u.IsAdmin,
		Enabled:            u.Enabled,
		MustChangePassword: u.MustChangePassword,
		CreatedAt:          u.CreatedAt,
		UpdatedAt:          u.UpdatedAt,
	}
}

// ListUsers handles GET /api/v1/users.
func (h *UsersHandler) ListUsers(c *gin.Context) {
	users, err := h.userRepo.ListUsers(c.Request.Context())
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to list users: "+err.Error())
		return
	}

	var safeList []safeUserResp
	for _, u := range users {
		safeList = append(safeList, toSafeUser(u))
	}
	api.SendSuccess(c, safeList)
}

type createUserReq struct {
	Username           string `json:"username" binding:"required"`
	Password           string `json:"password" binding:"required"`
	IsAdmin            int    `json:"is_admin"`
	MustChangePassword int    `json:"must_change_password"`
}

// CreateUser handles POST /api/v1/users.
func (h *UsersHandler) CreateUser(c *gin.Context) {
	var req createUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "username and password are required")
		return
	}

	if len(req.Password) < 6 {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "password must be at least 6 characters")
		return
	}

	ctx := c.Request.Context()
	existing, err := h.userRepo.GetUserByUsername(ctx, req.Username)
	if err == nil && existing != nil {
		api.SendError(c, http.StatusConflict, "username_exists", "username already taken")
		return
	}

	hash, err := db.HashPassword(req.Password)
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "hash_failed", "failed to hash password")
		return
	}

	u := &models.User{
		ID:                 "usr_" + uuid.New().String()[:8],
		Username:           req.Username,
		PasswordHash:       hash,
		IsAdmin:            req.IsAdmin,
		Enabled:            1,
		MustChangePassword: req.MustChangePassword,
	}

	if err := h.userRepo.CreateUser(ctx, u); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to create user: "+err.Error())
		return
	}

	api.SendSuccess(c, toSafeUser(*u))
}

type updateUserReq struct {
	Enabled *int `json:"enabled"`
	IsAdmin *int `json:"is_admin"`
}

// UpdateUser handles PUT /api/v1/users/:id.
// Protects the last administrator from being disabled or demoted.
func (h *UsersHandler) UpdateUser(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()

	target, err := h.userRepo.GetUserByID(ctx, id)
	if err != nil || target == nil {
		api.SendError(c, http.StatusNotFound, "user_not_found", "user not found")
		return
	}

	var req updateUserReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "invalid request payload")
		return
	}

	// Protection for unique administrator
	if target.IsAdmin == 1 {
		demoting := req.IsAdmin != nil && *req.IsAdmin == 0
		disabling := req.Enabled != nil && *req.Enabled == 0
		if demoting || disabling {
			adminCount, err := h.userRepo.CountEnabledAdmins(ctx)
			if err != nil || adminCount <= 1 {
				api.SendError(c, http.StatusBadRequest, "admin_protection", "cannot disable or remove administrator role from the only active administrator")
				return
			}
		}
	}

	if req.Enabled != nil {
		target.Enabled = *req.Enabled
		if target.Enabled == 0 {
			_ = h.userRepo.RevokeAllUserSessions(ctx, target.ID)
		}
	}
	if req.IsAdmin != nil {
		target.IsAdmin = *req.IsAdmin
	}

	if err := h.userRepo.UpdateUser(ctx, target); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to update user: "+err.Error())
		return
	}

	api.SendSuccess(c, toSafeUser(*target))
}

// DeleteUser handles DELETE /api/v1/users/:id.
// Protects the last administrator from being deleted.
func (h *UsersHandler) DeleteUser(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()

	target, err := h.userRepo.GetUserByID(ctx, id)
	if err != nil || target == nil {
		api.SendError(c, http.StatusNotFound, "user_not_found", "user not found")
		return
	}

	if target.IsAdmin == 1 {
		adminCount, err := h.userRepo.CountEnabledAdmins(ctx)
		if err != nil || adminCount <= 1 {
			api.SendError(c, http.StatusBadRequest, "admin_protection", "cannot delete the only active administrator")
			return
		}
	}

	if err := h.userRepo.DeleteUser(ctx, id); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to delete user: "+err.Error())
		return
	}

	api.SendSuccess(c, gin.H{"deleted": true, "user_id": id})
}

type resetPasswordReq struct {
	NewPassword string `json:"new_password" binding:"required"`
}

// ResetPassword handles POST /api/v1/users/:id/reset_password.
func (h *UsersHandler) ResetPassword(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()

	var req resetPasswordReq
	if err := c.ShouldBindJSON(&req); err != nil || len(req.NewPassword) < 6 {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "new_password must be at least 6 characters")
		return
	}

	target, err := h.userRepo.GetUserByID(ctx, id)
	if err != nil || target == nil {
		api.SendError(c, http.StatusNotFound, "user_not_found", "user not found")
		return
	}

	if err := h.userRepo.UpdatePassword(ctx, id, req.NewPassword); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to reset password")
		return
	}

	_ = h.userRepo.RevokeAllUserSessions(ctx, id)

	api.SendSuccess(c, gin.H{"reset": true, "user_id": id})
}
