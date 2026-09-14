package admin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"mediavault/internal/api"
	"mediavault/internal/db"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type ScraperHandler struct {
	database *db.DB
	jobRepo  *db.JobRepo
}

func NewScraperHandler(database *db.DB, jobRepo *db.JobRepo) *ScraperHandler {
	return &ScraperHandler{
		database: database,
		jobRepo:  jobRepo,
	}
}

type triggerScrapeReq struct {
	DateField     string `json:"date_field"` // release_date, publish_date
	StartDate     string `json:"start_date"`
	EndDate       string `json:"end_date"`
	IncludeFailed bool   `json:"include_failed"`
	IncludeExempt bool   `json:"include_exempt"`
	Limit         int    `json:"limit"`
}

// TriggerScrape handles POST /api/v1/scraper/trigger.
func (h *ScraperHandler) TriggerScrape(c *gin.Context) {
	var req triggerScrapeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "invalid request payload")
		return
	}

	dateField := "release_date"
	if req.DateField == "publish_date" {
		dateField = "publish_date"
	}

	ctx := c.Request.Context()

	// Count candidates matching filters
	where := "deleted_at IS NULL"
	var args []interface{}

	if !req.IncludeExempt {
		where += " AND scrape_policy != 'exempt'"
	}
	if !req.IncludeFailed {
		where += " AND scrape_status != 'not_found'"
	} else {
		where += " AND (scrape_status IN ('idle', 'transient', 'partial', 'not_found'))"
	}

	if req.StartDate != "" {
		where += fmt.Sprintf(" AND %s >= ?", dateField)
		args = append(args, req.StartDate)
	}
	if req.EndDate != "" {
		where += fmt.Sprintf(" AND %s <= ?", dateField)
		args = append(args, req.EndDate)
	}

	var count int
	err := h.database.ExecRead(ctx, func(d *sql.DB) error {
		query := fmt.Sprintf("SELECT COUNT(*) FROM offline_movies WHERE %s", where)
		return d.QueryRowContext(ctx, query, args...).Scan(&count)
	})
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to query candidates: "+err.Error())
		return
	}

	paramsBytes, _ := json.Marshal(gin.H{
		"date_field":     dateField,
		"start_date":     req.StartDate,
		"end_date":       req.EndDate,
		"include_failed": req.IncludeFailed,
		"include_exempt": req.IncludeExempt,
		"limit":          req.Limit,
		"candidates":     count,
	})

	job := &models.Job{
		ID:         "job_scrape_" + uuid.New().String()[:8],
		Kind:       "scrape",
		DedupeKey:  "scrape_batch",
		State:      "queued",
		Generation: 1,
		ParamsJSON: string(paramsBytes),
		ResultJSON: "{}",
	}

	activeJob, created, err := h.jobRepo.CreateOrGetJob(ctx, job)
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to enqueue scrape job: "+err.Error())
		return
	}

	status := "queued"
	if !created {
		status = "reused_active"
	}

	c.JSON(http.StatusAccepted, gin.H{
		"status":           status,
		"task_id":          activeJob.ID,
		"candidates_count": count,
	})
}

// GetScraperStatus handles GET /api/v1/scraper/status.
func (h *ScraperHandler) GetScraperStatus(c *gin.Context) {
	ctx := c.Request.Context()

	var idle, success, partial, notFound, transient int
	var auto, exempt, paused int

	err := h.database.ExecRead(ctx, func(d *sql.DB) error {
		_ = d.QueryRowContext(ctx, `
			SELECT
				COALESCE(SUM(CASE WHEN scrape_status = 'idle' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'success' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'partial' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'not_found' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'transient' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_policy = 'auto' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_policy = 'exempt' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_policy = 'paused' THEN 1 ELSE 0 END), 0)
			FROM offline_movies WHERE deleted_at IS NULL;
		`).Scan(&idle, &success, &partial, &notFound, &transient, &auto, &exempt, &paused)
		return nil
	})

	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to get scraper status: "+err.Error())
		return
	}

	// Check running scrape job
	jobs, _, _ := h.jobRepo.ListJobs(ctx, db.ListJobsParams{Kind: "scrape", State: "running", Limit: 1})
	var activeJob *models.Job
	if len(jobs) > 0 {
		activeJob = &jobs[0]
	}

	api.SendSuccess(c, gin.H{
		"scrape_status": gin.H{
			"idle":      idle,
			"success":   success,
			"partial":   partial,
			"not_found": notFound,
			"transient": transient,
		},
		"policy": gin.H{
			"auto":   auto,
			"exempt": exempt,
			"paused": paused,
		},
		"active_job": activeJob,
	})
}
