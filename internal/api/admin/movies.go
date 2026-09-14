package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"mediavault/internal/api"
	"mediavault/internal/db"
	"mediavault/internal/models"
	"mediavault/internal/services"

	"github.com/gin-gonic/gin"
)

type MoviesHandler struct {
	movieRepo       *db.MovieRepo
	magnetRepo      *db.MagnetRepo
	assetRepo       *db.AssetRepo
	transferManager *services.TransferManager
}

func NewMoviesHandler(
	movieRepo *db.MovieRepo,
	magnetRepo *db.MagnetRepo,
	assetRepo *db.AssetRepo,
	transferManager *services.TransferManager,
) *MoviesHandler {
	return &MoviesHandler{
		movieRepo:       movieRepo,
		magnetRepo:      magnetRepo,
		assetRepo:       assetRepo,
		transferManager: transferManager,
	}
}

// ListMovies handles GET /api/v1/movies.
func (h *MoviesHandler) ListMovies(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit < 1 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}
	offset := (page - 1) * limit

	params := db.ListMoviesParams{
		Limit:         limit,
		Offset:        offset,
		SortBy:        c.DefaultQuery("sort_by", "release"),
		SortOrder:     strings.ToUpper(c.DefaultQuery("sort_order", "DESC")),
		Category:      c.Query("category"),
		LibraryFilter: c.Query("library"),
		SearchTerm:    c.Query("q"),
	}

	movies, total, err := h.movieRepo.ListMovies(c.Request.Context(), params)
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to list movies: "+err.Error())
		return
	}

	api.SendSuccess(c, gin.H{
		"items": movies,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// GetMovie handles GET /api/v1/movies/:code.
func (h *MoviesHandler) GetMovie(c *gin.Context) {
	code := c.Param("code")
	ctx := c.Request.Context()

	movie, err := h.movieRepo.GetMovie(ctx, code)
	if err != nil {
		api.SendError(c, http.StatusNotFound, "not_found", "movie not found")
		return
	}

	magnets, err := h.magnetRepo.ListMagnetsByMovie(ctx, code)
	if err != nil {
		magnets = []models.Magnet{}
	}

	assets, err := h.assetRepo.ListAssetsForMovie(ctx, code)
	if err != nil {
		assets = []models.CloudAsset{}
	}

	api.SendSuccess(c, gin.H{
		"movie":   movie,
		"magnets": magnets,
		"assets":  assets,
	})
}

type updateMovieReq struct {
	Title         *string  `json:"title,omitempty"`
	TitleZh       *string  `json:"title_zh,omitempty"`
	DescriptionZh *string  `json:"description_zh,omitempty"`
	Category      *string  `json:"category,omitempty"`
	ReleaseDate   *string  `json:"release_date,omitempty"`
	PublishDate   *string  `json:"publish_date,omitempty"`
	CoverURL      *string  `json:"cover_url,omitempty"`
	PosterURL     *string  `json:"poster_url,omitempty"`
	Actors        *string  `json:"actors,omitempty"` // JSON array string or comma-separated
	Tags          *string  `json:"tags,omitempty"`
	Maker         *string  `json:"maker,omitempty"`
	Director      *string  `json:"director,omitempty"`
	Score         *float64 `json:"score,omitempty"`
	ScrapePolicy  *string  `json:"scrape_policy,omitempty"`
	PolicyReason  *string  `json:"policy_reason,omitempty"`
}

// UpdateMovie handles PUT /api/v1/movies/:code.
// Locks updated fields in manual_fields JSON array.
func (h *MoviesHandler) UpdateMovie(c *gin.Context) {
	code := c.Param("code")
	ctx := c.Request.Context()

	movie, err := h.movieRepo.GetMovie(ctx, code)
	if err != nil || movie == nil {
		api.SendError(c, http.StatusNotFound, "not_found", "movie not found")
		return
	}

	var req updateMovieReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "invalid request payload")
		return
	}

	// Parse existing manual fields
	var manualFields []string
	if movie.ManualFields != "" && movie.ManualFields != "[]" {
		_ = json.Unmarshal([]byte(movie.ManualFields), &manualFields)
	}
	manualMap := make(map[string]bool)
	for _, f := range manualFields {
		manualMap[f] = true
	}

	addManual := func(field string) {
		if !manualMap[field] {
			manualMap[field] = true
			manualFields = append(manualFields, field)
		}
	}

	if req.Title != nil {
		movie.Title = *req.Title
		addManual("title")
	}
	if req.TitleZh != nil {
		movie.TitleZh = req.TitleZh
		addManual("title_zh")
	}
	if req.DescriptionZh != nil {
		movie.DescriptionZh = req.DescriptionZh
		addManual("description_zh")
	}
	if req.Category != nil {
		movie.Category = *req.Category
		addManual("category")
	}
	if req.ReleaseDate != nil {
		movie.ReleaseDate = req.ReleaseDate
		addManual("release_date")
	}
	if req.PublishDate != nil {
		movie.PublishDate = req.PublishDate
		addManual("publish_date")
	}
	if req.CoverURL != nil {
		movie.CoverURL = req.CoverURL
		addManual("cover_url")
	}
	if req.PosterURL != nil {
		movie.PosterURL = req.PosterURL
		addManual("poster_url")
	}
	if req.Actors != nil {
		movie.Actors = *req.Actors
		addManual("actors")
	}
	if req.Tags != nil {
		movie.Tags = *req.Tags
		addManual("tags")
	}
	if req.Maker != nil {
		movie.Maker = req.Maker
		addManual("maker")
	}
	if req.Director != nil {
		movie.Director = req.Director
		addManual("director")
	}
	if req.Score != nil {
		if *req.Score < 0 || *req.Score > 5 {
			api.SendError(c, http.StatusBadRequest, "invalid_score", "score must be between 0 and 5")
			return
		}
		movie.Score = req.Score
		addManual("score")
	}
	if req.ScrapePolicy != nil {
		movie.ScrapePolicy = *req.ScrapePolicy
	}
	if req.PolicyReason != nil {
		movie.PolicyReason = req.PolicyReason
	}

	manualJSON, _ := json.Marshal(manualFields)
	movie.ManualFields = string(manualJSON)

	if err := h.movieRepo.UpsertMovie(ctx, movie); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to update movie: "+err.Error())
		return
	}

	api.SendSuccess(c, movie)
}

// DeleteMovie handles DELETE /api/v1/movies/:code (soft delete).
func (h *MoviesHandler) DeleteMovie(c *gin.Context) {
	code := c.Param("code")
	ctx := c.Request.Context()

	if err := h.movieRepo.SoftDeleteMovie(ctx, code); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to soft delete movie: "+err.Error())
		return
	}

	api.SendSuccess(c, gin.H{"deleted": true, "code": code})
}

type addMagnetReq struct {
	InfoHash      string `json:"info_hash" binding:"required"`
	MagnetURL     string `json:"magnet_url" binding:"required"`
	Title         string `json:"title"`
	SizeBytes     int64  `json:"size_bytes"`
	QualityLabel  string `json:"quality_label"`
	HasChineseSub int    `json:"has_chinese_sub"`
	Is4K          int    `json:"is_4k"`
	IsCensored    int    `json:"is_censored"`
	Enabled       int    `json:"enabled"`
	IsPreferred   int    `json:"is_preferred"`
}

// AddMagnet handles POST /api/v1/movies/:code/magnets.
func (h *MoviesHandler) AddMagnet(c *gin.Context) {
	code := c.Param("code")
	ctx := c.Request.Context()

	var req addMagnetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "info_hash and magnet_url are required")
		return
	}

	if req.QualityLabel == "" {
		req.QualityLabel = "HD"
	}
	if req.Enabled == 0 {
		req.Enabled = 1
	}

	mag := &models.Magnet{
		InfoHash:      strings.ToLower(req.InfoHash),
		MovieCode:     code,
		ResourceKind:  "btih",
		MagnetURL:     req.MagnetURL,
		Title:         &req.Title,
		SizeBytes:     req.SizeBytes,
		QualityLabel:  req.QualityLabel,
		HasChineseSub: req.HasChineseSub,
		Is4K:          req.Is4K,
		IsCensored:    req.IsCensored,
		Enabled:       req.Enabled,
		IsPreferred:   req.IsPreferred,
	}

	if err := h.magnetRepo.UpsertMagnet(ctx, mag); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to add magnet: "+err.Error())
		return
	}

	if req.IsPreferred == 1 {
		_ = h.magnetRepo.SetPreferredMagnet(ctx, code, mag.InfoHash)
	}

	api.SendSuccess(c, mag)
}

type toggleMagnetReq struct {
	Enabled     *int `json:"enabled,omitempty"`
	IsPreferred *int `json:"is_preferred,omitempty"`
}

// ToggleMagnet handles PUT /api/v1/movies/:code/magnets/:hash.
func (h *MoviesHandler) ToggleMagnet(c *gin.Context) {
	code := c.Param("code")
	hash := strings.ToLower(c.Param("hash"))
	ctx := c.Request.Context()

	var req toggleMagnetReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "invalid request payload")
		return
	}

	if req.IsPreferred != nil && *req.IsPreferred == 1 {
		if err := h.magnetRepo.SetPreferredMagnet(ctx, code, hash); err != nil {
			api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to set preferred magnet: "+err.Error())
			return
		}
	}

	api.SendSuccess(c, gin.H{"updated": true, "info_hash": hash})
}

// PrepareMovie handles POST /api/v1/movies/:code/prepare (manual cold download trigger).
func (h *MoviesHandler) PrepareMovie(c *gin.Context) {
	code := c.Param("code")
	ctx := c.Request.Context()

	if h.transferManager == nil {
		api.SendError(c, http.StatusServiceUnavailable, "transfer_unavailable", "transfer manager not initialized")
		return
	}

	binding, err := h.assetRepo.GetActiveBinding(ctx, "115")
	if err != nil || binding == nil {
		api.SendError(c, http.StatusBadRequest, "binding_not_found", "115 cloud account is not bound")
		return
	}

	now := models.UTCNow()
	res, err := h.magnetRepo.ResolveDefaultSource(ctx, code, now)
	if err != nil || res == nil || res.Magnet == nil {
		api.SendError(c, http.StatusBadRequest, "no_magnet_available", "no transferable magnet found for movie "+code)
		return
	}

	if res.Tier == 1 || res.Tier == 2 {
		// Asset is already ready
		api.SendSuccess(c, gin.H{
			"status":   "already_ready",
			"tier":     res.Tier,
			"asset_id": res.CloudAsset.ID,
		})
		return
	}

	job, err := h.transferManager.StartTransfer(ctx, binding, res.Magnet)
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "transfer_failed", "failed to start transfer: "+err.Error())
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"status":     "transfer_started",
		"job_id":     job.ID,
		"state":      job.State,
		"movie_code": code,
		"info_hash":  res.Magnet.InfoHash,
	})
}
