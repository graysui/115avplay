package admin

import (
	"net/http"

	"mediavault/internal/api"
	"mediavault/internal/db"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
)

type LibrariesHandler struct {
	libraryRepo *db.LibraryRepo
}

func NewLibrariesHandler(libraryRepo *db.LibraryRepo) *LibrariesHandler {
	return &LibrariesHandler{libraryRepo: libraryRepo}
}

// ListLibraries handles GET /api/v1/libraries.
func (h *LibrariesHandler) ListLibraries(c *gin.Context) {
	libs, err := h.libraryRepo.ListLibraries(c.Request.Context())
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to list libraries: "+err.Error())
		return
	}
	api.SendSuccess(c, libs)
}

type updateLibraryReq struct {
	Name      string  `json:"name" binding:"required"`
	SortOrder int     `json:"sort_order"`
	Enabled   int     `json:"enabled"`
	CoverURL  *string `json:"cover_url"`
}

// UpdateLibrary handles PUT /api/v1/libraries/:id.
func (h *LibrariesHandler) UpdateLibrary(c *gin.Context) {
	id := c.Param("id")
	var req updateLibraryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "name is required")
		return
	}

	lib := &models.Library{
		ID:        id,
		Name:      req.Name,
		SortOrder: req.SortOrder,
		Enabled:   req.Enabled,
		CoverURL:  req.CoverURL,
	}

	if err := h.libraryRepo.UpdateLibrary(c.Request.Context(), lib); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to update library: "+err.Error())
		return
	}

	api.SendSuccess(c, lib)
}
