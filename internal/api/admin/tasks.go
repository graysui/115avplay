package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"mediavault/internal/api"
	"mediavault/internal/db"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type TasksHandler struct {
	jobRepo *db.JobRepo
}

func NewTasksHandler(jobRepo *db.JobRepo) *TasksHandler {
	return &TasksHandler{jobRepo: jobRepo}
}

// ListTasks handles GET /api/v1/tasks.
func (h *TasksHandler) ListTasks(c *gin.Context) {
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

	params := db.ListJobsParams{
		Kind:   c.Query("kind"),
		State:  c.Query("state"),
		Limit:  limit,
		Offset: offset,
	}

	jobs, total, err := h.jobRepo.ListJobs(c.Request.Context(), params)
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to list tasks: "+err.Error())
		return
	}

	api.SendSuccess(c, gin.H{
		"items": jobs,
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

// GetTask handles GET /api/v1/tasks/:id.
func (h *TasksHandler) GetTask(c *gin.Context) {
	id := c.Param("id")
	job, err := h.jobRepo.GetJobByID(c.Request.Context(), id)
	if err != nil || job == nil {
		api.SendError(c, http.StatusNotFound, "task_not_found", "task not found")
		return
	}
	api.SendSuccess(c, job)
}

// RetryTask handles POST /api/v1/tasks/:id/retry.
func (h *TasksHandler) RetryTask(c *gin.Context) {
	id := c.Param("id")
	if err := h.jobRepo.RetryJob(c.Request.Context(), id); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to retry task: "+err.Error())
		return
	}
	api.SendSuccess(c, gin.H{"retried": true, "task_id": id})
}

// CancelTask handles POST /api/v1/tasks/:id/cancel.
func (h *TasksHandler) CancelTask(c *gin.Context) {
	id := c.Param("id")
	if err := h.jobRepo.CancelJob(c.Request.Context(), id); err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to cancel task: "+err.Error())
		return
	}
	api.SendSuccess(c, gin.H{"cancelled": true, "task_id": id})
}

type triggerTaskReq struct {
	Kind   string                 `json:"kind" binding:"required"` // sync30d, import_full, rankings, scan
	Params map[string]interface{} `json:"params"`
}

// TriggerTask handles POST /api/v1/tasks/trigger.
// Reuses active task if already running/queued (T-706: 复用活动任务).
func (h *TasksHandler) TriggerTask(c *gin.Context) {
	var req triggerTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "kind is required")
		return
	}

	validKinds := map[string]bool{
		"sync30d":     true,
		"import_full": true,
		"rankings":    true,
		"scan":        true,
		"scrape":      true,
		"transfer":    true,
		"clean":       true,
	}
	if !validKinds[req.Kind] {
		api.SendError(c, http.StatusBadRequest, "invalid_kind", "unsupported task kind: "+req.Kind)
		return
	}

	paramsBytes, _ := json.Marshal(req.Params)
	dedupeKey := req.Kind // Default dedupe key is kind itself to coalesce duplicate triggers

	job := &models.Job{
		ID:         "job_" + req.Kind + "_" + uuid.New().String()[:8],
		Kind:       req.Kind,
		DedupeKey:  dedupeKey,
		State:      "queued",
		Generation: 1,
		ParamsJSON: string(paramsBytes),
		ResultJSON: "{}",
	}

	existingOrNew, created, err := h.jobRepo.CreateOrGetJob(c.Request.Context(), job)
	if err != nil {
		api.SendError(c, http.StatusInternalServerError, "internal_error", "failed to trigger task: "+err.Error())
		return
	}

	status := "queued"
	if !created {
		status = "reused_active"
	}

	c.JSON(http.StatusAccepted, gin.H{
		"status":  status,
		"task_id": existingOrNew.ID,
		"kind":    existingOrNew.Kind,
		"state":   existingOrNew.State,
	})
}
