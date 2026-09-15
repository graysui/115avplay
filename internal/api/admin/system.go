package admin

import (
	"database/sql"

	"mediavault/internal/api"
	"mediavault/internal/db"

	"github.com/gin-gonic/gin"
)

// SystemHandler exposes schema/migration information for the admin console.
type SystemHandler struct {
	database *db.DB
}

func NewSystemHandler(database *db.DB) *SystemHandler {
	return &SystemHandler{database: database}
}

// GetSchema handles GET /api/v1/system/schema.
// It reports the detected schema state so operators can see whether a legacy
// database has been migrated and how many rows were imported.
func (h *SystemHandler) GetSchema(c *gin.Context) {
	ctx := c.Request.Context()

	var isInit, isLegacy bool
	var version int
	var detectErr error
	_ = h.database.ExecRead(ctx, func(d *sql.DB) error {
		isInit, isLegacy, version, detectErr = db.DetectSchemaState(ctx, d)
		return nil
	})

	resp := gin.H{
		"initialized":    isInit,
		"legacy_v0":      isLegacy,
		"version":        version,
		"target_version": db.CurrentSchemaVersion,
	}

	if detectErr != nil {
		resp["error"] = detectErr.Error()
	}

	if isInit {
		meta, err := db.ReadSchemaMeta(ctx, h.database.Reader())
		if err == nil && meta != nil {
			resp["server_id"] = meta.ServerID
			resp["migrated_at"] = meta.MigratedAt
			resp["checksum"] = meta.Checksum
		}
	}

	// Row counts help confirm an import/migration succeeded.
	var movies, magnets int
	_ = h.database.ExecRead(ctx, func(d *sql.DB) error {
		_ = d.QueryRowContext(ctx, "SELECT COUNT(*) FROM offline_movies WHERE deleted_at IS NULL;").Scan(&movies)
		_ = d.QueryRowContext(ctx, "SELECT COUNT(*) FROM offline_magnets;").Scan(&magnets)
		return nil
	})
	resp["movies"] = movies
	resp["magnets"] = magnets

	api.SendSuccess(c, resp)
}
