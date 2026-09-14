package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mediavault/internal/models"
	"time"
)

type SyncWatermark struct {
	ReleaseID    string   `json:"release_id"`
	CoverageDate string   `json:"coverage_date"`
	Sources      []string `json:"sources"`
	SyncGap      bool     `json:"sync_gap,omitempty"`
	UpdatedAt    string   `json:"updated_at"`
}

type IngestRepo struct {
	db *DB
}

func NewIngestRepo(db *DB) *IngestRepo {
	return &IngestRepo{db: db}
}

// UpsertIngestAsset creates or updates an ingest_assets record.
func (r *IngestRepo) UpsertIngestAsset(ctx context.Context, asset *models.IngestAsset) error {
	now := models.UTCNow()
	asset.UpdatedAt = now
	if asset.CountsJSON == "" {
		asset.CountsJSON = "{}"
	}

	query := `
		INSERT INTO ingest_assets (
			id, run_id, release_id, source, asset_name, sha256, size_bytes,
			coverage_start, coverage_end, state, last_committed_row, counts_json,
			last_error, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(release_id, source, sha256) DO UPDATE SET
			run_id = excluded.run_id,
			state = excluded.state,
			last_committed_row = excluded.last_committed_row,
			counts_json = excluded.counts_json,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at;
	`
	_, err := r.db.Writer().ExecContext(ctx, query,
		asset.ID, asset.RunID, asset.ReleaseID, asset.Source, asset.AssetName,
		asset.SHA256, asset.SizeBytes, asset.CoverageStart, asset.CoverageEnd,
		asset.State, asset.LastCommittedRow, asset.CountsJSON, asset.LastError,
		asset.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert ingest asset: %w", err)
	}
	return nil
}

// GetIngestAsset retrieves an asset by release_id and source.
func (r *IngestRepo) GetIngestAsset(ctx context.Context, releaseID, source string) (*models.IngestAsset, error) {
	query := `
		SELECT id, run_id, release_id, source, asset_name, sha256, size_bytes,
		       coverage_start, coverage_end, state, last_committed_row, counts_json,
		       last_error, updated_at
		FROM ingest_assets
		WHERE release_id = ? AND source = ?
		ORDER BY updated_at DESC LIMIT 1;
	`
	row := r.db.Reader().QueryRowContext(ctx, query, releaseID, source)

	var a models.IngestAsset
	var covStart, covEnd, lastErr sql.NullString
	err := row.Scan(
		&a.ID, &a.RunID, &a.ReleaseID, &a.Source, &a.AssetName, &a.SHA256, &a.SizeBytes,
		&covStart, &covEnd, &a.State, &a.LastCommittedRow, &a.CountsJSON,
		&lastErr, &a.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get ingest asset: %w", err)
	}
	if covStart.Valid {
		a.CoverageStart = &covStart.String
	}
	if covEnd.Valid {
		a.CoverageEnd = &covEnd.String
	}
	if lastErr.Valid {
		a.LastError = &lastErr.String
	}
	return &a, nil
}

// UpdateIngestAssetState updates the state, row count and error of an ingest asset.
func (r *IngestRepo) UpdateIngestAssetState(ctx context.Context, id, state string, lastCommittedRow int, countsJSON string, lastError *string) error {
	now := models.UTCNow()
	if countsJSON == "" {
		countsJSON = "{}"
	}
	query := `
		UPDATE ingest_assets
		SET state = ?, last_committed_row = ?, counts_json = ?, last_error = ?, updated_at = ?
		WHERE id = ?;
	`
	_, err := r.db.Writer().ExecContext(ctx, query, state, lastCommittedRow, countsJSON, lastError, now, id)
	if err != nil {
		return fmt.Errorf("update ingest asset state: %w", err)
	}
	return nil
}

// AreReleaseAssetsCompleted checks if all specified sources for a release are completed.
func (r *IngestRepo) AreReleaseAssetsCompleted(ctx context.Context, releaseID string, sources []string) (bool, error) {
	for _, src := range sources {
		query := `
			SELECT COUNT(1) FROM ingest_assets
			WHERE release_id = ? AND source = ? AND state = 'completed';
		`
		var count int
		if err := r.db.Reader().QueryRowContext(ctx, query, releaseID, src).Scan(&count); err != nil {
			return false, fmt.Errorf("check source completion: %w", err)
		}
		if count == 0 {
			return false, nil
		}
	}
	return true, nil
}

// GetSyncWatermark reads sync_watermark from system_settings.
func (r *IngestRepo) GetSyncWatermark(ctx context.Context) (*SyncWatermark, error) {
	query := `SELECT value FROM system_settings WHERE key = 'sync_watermark';`
	var val string
	err := r.db.Reader().QueryRowContext(ctx, query).Scan(&val)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get sync watermark: %w", err)
	}

	if val == "" || val == "{}" {
		return nil, nil
	}

	var wm SyncWatermark
	if err := json.Unmarshal([]byte(val), &wm); err != nil {
		return nil, fmt.Errorf("unmarshal sync watermark: %w", err)
	}
	return &wm, nil
}

// UpdateSyncWatermark saves sync_watermark to system_settings.
func (r *IngestRepo) UpdateSyncWatermark(ctx context.Context, wm *SyncWatermark) error {
	wm.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.Marshal(wm)
	if err != nil {
		return fmt.Errorf("marshal sync watermark: %w", err)
	}

	query := `
		INSERT INTO system_settings (key, value, is_secret, revision, updated_at)
		VALUES ('sync_watermark', ?, 0, 1, ?)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			revision = system_settings.revision + 1,
			updated_at = excluded.updated_at;
	`
	_, err = r.db.Writer().ExecContext(ctx, query, string(data), wm.UpdatedAt)
	if err != nil {
		return fmt.Errorf("update sync watermark: %w", err)
	}
	return nil
}
