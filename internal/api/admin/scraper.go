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
	runner   TaskRunner
}

func NewScraperHandler(database *db.DB, jobRepo *db.JobRepo, runner TaskRunner) *ScraperHandler {
	return &ScraperHandler{
		database: database,
		jobRepo:  jobRepo,
		runner:   runner,
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

	// Dispatch to the real batch scraper when the runtime is available.
	if h.runner != nil {
		taskID, count, err := h.runner.TriggerScrape(ctx, dateField, req.StartDate, req.EndDate, req.IncludeFailed, req.IncludeExempt, req.Limit)
		if err != nil {
			api.SendError(c, http.StatusInternalServerError, "trigger_failed", err.Error())
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"status":           "queued",
			"task_id":          taskID,
			"candidates_count": count,
		})
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

	// Optional date-range filters (same semantics as the trigger form).
	dateField := "release_date"
	if c.Query("date_field") == "publish_date" {
		dateField = "publish_date"
	}
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")
	includeExempt := c.Query("include_exempt") == "true" || c.Query("include_exempt") == "1"

	dateClause := ""
	var args []interface{}
	if startDate != "" {
		dateClause += fmt.Sprintf(" AND %s >= ?", dateField)
		args = append(args, startDate)
	}
	if endDate != "" {
		dateClause += fmt.Sprintf(" AND %s <= ?", dateField)
		args = append(args, endDate)
	}
	if !includeExempt {
		dateClause += " AND scrape_policy != 'exempt'"
	}

	var pending, success, partial, notFound, transient, paused int
	var total, withCover, withTitleZH, withDesc int

	err := h.database.ExecRead(ctx, func(d *sql.DB) error {
		_ = d.QueryRowContext(ctx, `
			SELECT
				COALESCE(SUM(CASE WHEN scrape_status = 'idle' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'success' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'partial' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'not_found' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_status = 'transient' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN scrape_policy = 'paused' THEN 1 ELSE 0 END), 0),
				COUNT(*),
				COALESCE(SUM(CASE WHEN cover_url IS NOT NULL AND cover_url != '' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN title_zh IS NOT NULL AND title_zh != '' THEN 1 ELSE 0 END), 0),
				COALESCE(SUM(CASE WHEN description_zh IS NOT NULL AND description_zh != '' THEN 1 ELSE 0 END), 0)
			FROM offline_movies WHERE deleted_at IS NULL`+dateClause+`;
		`, args...).Scan(&pending, &success, &partial, &notFound, &transient, &paused,
			&total, &withCover, &withTitleZH, &withDesc)
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
			"idle":      pending, // 待刮削：元数据不完整（缺封面或标题）
			"success":   success, // 元数据已完整
			"partial":   partial,
			"not_found": notFound,
			"transient": transient,
		},
		"policy": gin.H{
			"paused": paused,
		},
		"missing": gin.H{
			"total":       total,
			"no_cover":    total - withCover,
			"no_title_zh": total - withTitleZH,
			"no_desc":     total - withDesc,
		},
		"filters": gin.H{
			"date_field": dateField,
			"start_date": startDate,
			"end_date":   endDate,
		},
		"active_job": activeJob,
	})
}
