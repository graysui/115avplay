package admin

import (
	"database/sql"
	"net/http"

	"mediavault/internal/api"
	"mediavault/internal/db"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
)

type StatsHandler struct {
	database *db.DB
}

func NewStatsHandler(database *db.DB) *StatsHandler {
	return &StatsHandler{database: database}
}

type statsResponse struct {
	Completeness completenessStats `json:"completeness"`
	Policy       policyStats       `json:"policy"`
	ScrapeStatus scrapeStatusStats `json:"scrape_status"`
	Media        mediaStats        `json:"media"`
	Jobs         jobStats          `json:"jobs"`
}

type completenessStats struct {
	Complete int `json:"complete"`
	Partial  int `json:"partial"`
	Raw      int `json:"raw"`
}

type policyStats struct {
	Auto   int `json:"auto"`
	Exempt int `json:"exempt"`
	Paused int `json:"paused"`
}

type scrapeStatusStats struct {
	Idle      int `json:"idle"`
	Success   int `json:"success"`
	Partial   int `json:"partial"`
	NotFound  int `json:"not_found"`
	Transient int `json:"transient"`
}

type mediaStats struct {
	TotalMovies       int   `json:"total_movies"`
	TotalMagnets      int   `json:"total_magnets"`
	ChineseSubMagnets int   `json:"chinese_sub_magnets"`
	FourKMagnets      int   `json:"four_k_magnets"`
	PermanentAssets   int   `json:"permanent_assets"`
	TemporaryAssets   int   `json:"temporary_assets"`
	TotalBytes        int64 `json:"total_bytes"`
}

type jobStats struct {
	Running int `json:"running"`
	Pending int `json:"pending"`
	Failed  int `json:"failed"`
}

// GetStats handles GET /api/v1/stats.
func (h *StatsHandler) GetStats(c *gin.Context) {
	now := models.UTCNow()
	var res statsResponse

	err := h.database.ExecRead(c.Request.Context(), func(d *sql.DB) error {
		// 1. Completeness stats
		_ = d.QueryRowContext(c.Request.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN cover_url IS NOT NULL AND title_zh IS NOT NULL AND description_zh IS NOT NULL AND actors != '[]' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN (cover_url IS NOT NULL OR title_zh IS NOT NULL) AND NOT (cover_url IS NOT NULL AND title_zh IS NOT NULL AND description_zh IS NOT NULL AND actors != '[]') THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN cover_url IS NULL AND title_zh IS NULL THEN 1 ELSE 0 END), 0)
			FROM offline_movies WHERE deleted_at IS NULL;
		`).Scan(&res.Completeness.Complete, &res.Completeness.Partial, &res.Completeness.Raw)

		// 2. Policy stats
		_ = d.QueryRowContext(c.Request.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN scrape_policy = 'auto' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_policy = 'exempt' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_policy = 'paused' THEN 1 ELSE 0 END), 0)
			FROM offline_movies WHERE deleted_at IS NULL;
		`).Scan(&res.Policy.Auto, &res.Policy.Exempt, &res.Policy.Paused)

		// 3. Scrape Status stats
		_ = d.QueryRowContext(c.Request.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN scrape_status = 'idle' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'success' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'partial' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'not_found' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'transient' THEN 1 ELSE 0 END), 0)
			FROM offline_movies WHERE deleted_at IS NULL;
		`).Scan(&res.ScrapeStatus.Idle, &res.ScrapeStatus.Success, &res.ScrapeStatus.Partial, &res.ScrapeStatus.NotFound, &res.ScrapeStatus.Transient)

		// 4. Media stats
		_ = d.QueryRowContext(c.Request.Context(), `SELECT COUNT(*) FROM offline_movies WHERE deleted_at IS NULL;`).Scan(&res.Media.TotalMovies)
		_ = d.QueryRowContext(c.Request.Context(), `
			SELECT
				COUNT(*),
				COALESCE(SUM(CASE WHEN has_chinese_sub = 1 THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN is_4k = 1 THEN 1 ELSE 0 END), 0)
			FROM offline_magnets;
		`).Scan(&res.Media.TotalMagnets, &res.Media.ChineseSubMagnets, &res.Media.FourKMagnets)

		_ = d.QueryRowContext(c.Request.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN source_type = 'permanent' AND state = 'ready' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN source_type = 'temporary' AND state = 'ready' AND expires_at > ? THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN state = 'ready' THEN size_bytes ELSE 0 END), 0)
			FROM cloud_assets;
		`, now).Scan(&res.Media.PermanentAssets, &res.Media.TemporaryAssets, &res.Media.TotalBytes)

		// 5. Job stats: pending counts schedulable records (queued or ready retry_wait)
		_ = d.QueryRowContext(c.Request.Context(), `
			SELECT
				COALESCE(SUM(CASE WHEN state = 'running' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN state = 'queued' OR (state = 'retry_wait' AND next_run_at <= ?) THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN state = 'failed' THEN 1 ELSE 0 END), 0)
			FROM jobs;
		`, now).Scan(&res.Jobs.Running, &res.Jobs.Pending, &res.Jobs.Failed)

		return nil
	})

	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to query statistics")
		return
	}

	api.SendSuccess(c, res)
}
