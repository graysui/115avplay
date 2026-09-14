package db

import (
	"context"
	"database/sql"
	"fmt"
	"mediavault/internal/models"
)

type AssetRepo struct {
	db *DB
}

func NewAssetRepo(db *DB) *AssetRepo {
	return &AssetRepo{db: db}
}

// GetActiveBinding returns the currently enabled cloud binding (e.g. provider='115').
func (r *AssetRepo) GetActiveBinding(ctx context.Context, provider string) (*models.CloudBinding, error) {
	query := `
		SELECT id, provider, provider_user_id, enabled, secret_setting_key, config_revision, created_at, updated_at
		FROM cloud_bindings
		WHERE provider = ? AND enabled = 1
		LIMIT 1;
	`
	var b models.CloudBinding
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		return database.QueryRowContext(ctx, query, provider).Scan(
			&b.ID, &b.Provider, &b.ProviderUserID, &b.Enabled, &b.SecretSettingKey,
			&b.ConfigRevision, &b.CreatedAt, &b.UpdatedAt,
		)
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get active binding: %w", err)
	}
	return &b, nil
}

// UpsertBinding inserts or updates a cloud binding.
func (r *AssetRepo) UpsertBinding(ctx context.Context, b *models.CloudBinding) error {
	now := models.UTCNow()
	if b.CreatedAt == "" {
		b.CreatedAt = now
	}
	b.UpdatedAt = now

	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cloud_bindings (id, provider, provider_user_id, enabled, secret_setting_key, config_revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				provider_user_id = excluded.provider_user_id,
				enabled = excluded.enabled,
				secret_setting_key = excluded.secret_setting_key,
				config_revision = excluded.config_revision,
				updated_at = excluded.updated_at;
		`, b.ID, b.Provider, b.ProviderUserID, b.Enabled, b.SecretSettingKey, b.ConfigRevision, b.CreatedAt, b.UpdatedAt)
		return err
	})
}

// UpsertAsset inserts or updates a cloud_assets row.
func (r *AssetRepo) UpsertAsset(ctx context.Context, a *models.CloudAsset) error {
	now := models.UTCNow()
	if a.CreatedAt == "" {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	if a.Generation <= 0 {
		a.Generation = 1
	}
	if a.ManifestJSON == "" {
		a.ManifestJSON = "[]"
	}

	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO cloud_assets (
				id, binding_id, resource_key, owning_job_id, source_type, state, generation,
				file_id, pick_code, file_name, size_bytes, container, runtime_ticks,
				media_streams, parent_id, owned_root_id, root_snapshot, manifest_json,
				ready_at, expires_at, deleted_at, last_seen_at, last_error, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				state = excluded.state,
				generation = cloud_assets.generation + 1,
				file_id = excluded.file_id,
				pick_code = excluded.pick_code,
				file_name = excluded.file_name,
				size_bytes = excluded.size_bytes,
				expires_at = excluded.expires_at,
				deleted_at = excluded.deleted_at,
				last_seen_at = excluded.last_seen_at,
				last_error = excluded.last_error,
				updated_at = excluded.updated_at;
		`,
			a.ID, a.BindingID, a.ResourceKey, a.OwningJobID, a.SourceType, a.State, a.Generation,
			a.FileID, a.PickCode, a.FileName, a.SizeBytes, a.Container, a.RuntimeTicks,
			a.MediaStreams, a.ParentID, a.OwnedRootID, a.RootSnapshot, a.ManifestJSON,
			a.ReadyAt, a.ExpiresAt, a.DeletedAt, a.LastSeenAt, a.LastError, a.CreatedAt, a.UpdatedAt,
		)
		return err
	})
}

func scanAsset(row rowScanner) (*models.CloudAsset, error) {
	var a models.CloudAsset
	var resKey, owningJob, fileID, pickCode, fileName, container, streams sql.NullString
	var parentID, ownedRoot, rootSnap, readyAt, expiresAt, deletedAt, lastSeenAt, lastErr sql.NullString
	var runtimeTicks sql.NullInt64

	err := row.Scan(
		&a.ID, &a.BindingID, &resKey, &owningJob, &a.SourceType, &a.State, &a.Generation,
		&fileID, &pickCode, &fileName, &a.SizeBytes, &container, &runtimeTicks,
		&streams, &parentID, &ownedRoot, &rootSnap, &a.ManifestJSON,
		&readyAt, &expiresAt, &deletedAt, &lastSeenAt, &lastErr, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if resKey.Valid {
		a.ResourceKey = &resKey.String
	}
	if owningJob.Valid {
		a.OwningJobID = &owningJob.String
	}
	if fileID.Valid {
		a.FileID = &fileID.String
	}
	if pickCode.Valid {
		a.PickCode = &pickCode.String
	}
	if fileName.Valid {
		a.FileName = &fileName.String
	}
	if container.Valid {
		a.Container = &container.String
	}
	if runtimeTicks.Valid {
		a.RuntimeTicks = &runtimeTicks.Int64
	}
	if streams.Valid {
		a.MediaStreams = &streams.String
	}
	if parentID.Valid {
		a.ParentID = &parentID.String
	}
	if ownedRoot.Valid {
		a.OwnedRootID = &ownedRoot.String
	}
	if rootSnap.Valid {
		a.RootSnapshot = &rootSnap.String
	}
	if readyAt.Valid {
		a.ReadyAt = &readyAt.String
	}
	if expiresAt.Valid {
		a.ExpiresAt = &expiresAt.String
	}
	if deletedAt.Valid {
		a.DeletedAt = &deletedAt.String
	}
	if lastSeenAt.Valid {
		a.LastSeenAt = &lastSeenAt.String
	}
	if lastErr.Valid {
		a.LastError = &lastErr.String
	}

	return &a, nil
}

// GetAssetByID retrieves a cloud asset by ID.
func (r *AssetRepo) GetAssetByID(ctx context.Context, id string) (*models.CloudAsset, error) {
	query := `
		SELECT id, binding_id, resource_key, owning_job_id, source_type, state, generation,
		       file_id, pick_code, file_name, size_bytes, container, runtime_ticks,
		       media_streams, parent_id, owned_root_id, root_snapshot, manifest_json,
		       ready_at, expires_at, deleted_at, last_seen_at, last_error, created_at, updated_at
		FROM cloud_assets
		WHERE id = ?;
	`
	var asset *models.CloudAsset
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		row := database.QueryRowContext(ctx, query, id)
		var err error
		asset, err = scanAsset(row)
		return err
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get asset %s: %w", id, err)
	}
	return asset, nil
}

// GetReadyAssetByResource returns the ready asset for a resource key and binding.
func (r *AssetRepo) GetReadyAssetByResource(ctx context.Context, resourceKey, bindingID, now string) (*models.CloudAsset, error) {
	query := `
		SELECT id, binding_id, resource_key, owning_job_id, source_type, state, generation,
		       file_id, pick_code, file_name, size_bytes, container, runtime_ticks,
		       media_streams, parent_id, owned_root_id, root_snapshot, manifest_json,
		       ready_at, expires_at, deleted_at, last_seen_at, last_error, created_at, updated_at
		FROM cloud_assets
		WHERE resource_key = ? AND binding_id = ? AND state = 'ready'
		  AND (source_type = 'permanent' OR (source_type = 'temporary' AND expires_at > ?))
		ORDER BY CASE WHEN source_type = 'permanent' THEN 1 ELSE 2 END ASC
		LIMIT 1;
	`
	var asset *models.CloudAsset
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		row := database.QueryRowContext(ctx, query, resourceKey, bindingID, now)
		var err error
		asset, err = scanAsset(row)
		return err
	})
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get ready asset: %w", err)
	}
	return asset, nil
}

// ListAssetsForMovie returns all assets associated with the movie's magnets.
func (r *AssetRepo) ListAssetsForMovie(ctx context.Context, movieCode string) ([]models.CloudAsset, error) {
	query := `
		SELECT a.id, a.binding_id, a.resource_key, a.owning_job_id, a.source_type, a.state, a.generation,
		       a.file_id, a.pick_code, a.file_name, a.size_bytes, a.container, a.runtime_ticks,
		       a.media_streams, a.parent_id, a.owned_root_id, a.root_snapshot, a.manifest_json,
		       a.ready_at, a.expires_at, a.deleted_at, a.last_seen_at, a.last_error, a.created_at, a.updated_at
		FROM cloud_assets a
		JOIN offline_magnets m ON m.info_hash = a.resource_key
		WHERE m.movie_code = ?
		ORDER BY a.created_at DESC;
	`
	var list []models.CloudAsset
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		rows, err := database.QueryContext(ctx, query, movieCode)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			asset, err := scanAsset(rows)
			if err != nil {
				return err
			}
			list = append(list, *asset)
		}
		return nil
	})
	return list, err
}

// UpdateAssetState updates state and generation for an asset.
func (r *AssetRepo) UpdateAssetState(ctx context.Context, id, state string, lastError *string) error {
	now := models.UTCNow()
	var deletedAt *string
	if state == "deleted" {
		deletedAt = &now
	}

	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE cloud_assets
			SET state = ?, generation = generation + 1, last_error = ?,
			    deleted_at = COALESCE(?, deleted_at), updated_at = ?
			WHERE id = ?;
		`, state, lastError, deletedAt, now, id)
		return err
	})
}

// ListExpiredTemporaryAssets returns temporary ready assets whose expires_at is before now.
func (r *AssetRepo) ListExpiredTemporaryAssets(ctx context.Context, now string) ([]models.CloudAsset, error) {
	query := `
		SELECT id, binding_id, resource_key, owning_job_id, source_type, state, generation,
		       file_id, pick_code, file_name, size_bytes, container, runtime_ticks,
		       media_streams, parent_id, owned_root_id, root_snapshot, manifest_json,
		       ready_at, expires_at, deleted_at, last_seen_at, last_error, created_at, updated_at
		FROM cloud_assets
		WHERE source_type = 'temporary' AND state = 'ready' AND expires_at IS NOT NULL AND expires_at <= ?
		ORDER BY expires_at ASC;
	`
	var list []models.CloudAsset
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		rows, err := database.QueryContext(ctx, query, now)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			a, err := scanAsset(rows)
			if err != nil {
				return err
			}
			list = append(list, *a)
		}
		return nil
	})
	return list, err
}

// HasActivePlayLease checks if an asset currently has active/negotiating play leases.
func (r *AssetRepo) HasActivePlayLease(ctx context.Context, assetID, now string) (bool, error) {
	query := `
		SELECT COUNT(1)
		FROM play_sessions
		WHERE asset_id = ? AND state IN ('negotiating', 'active')
		  AND lease_until IS NOT NULL AND lease_until > ?;
	`
	var count int
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		return database.QueryRowContext(ctx, query, assetID, now).Scan(&count)
	})
	if err != nil {
		return false, fmt.Errorf("check active play lease: %w", err)
	}
	return count > 0, nil
}

// CreateScanRun inserts a new scan_runs row.
func (r *AssetRepo) CreateScanRun(ctx context.Context, s *models.ScanRun) error {
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO scan_runs (id, job_id, binding_id, root_id, mode, state, started_at, completed_at, last_error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);
		`, s.ID, s.JobID, s.BindingID, s.RootID, s.Mode, s.State, s.StartedAt, s.CompletedAt, s.LastError)
		return err
	})
}

// CompleteScanRun marks a scan run completed or failed.
func (r *AssetRepo) CompleteScanRun(ctx context.Context, id, state string, lastError *string) error {
	now := models.UTCNow()
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE scan_runs
			SET state = ?, completed_at = ?, last_error = ?
			WHERE id = ?;
		`, state, now, lastError, id)
		return err
	})
}

// RecordScanSeen records a file seen during a scan.
func (r *AssetRepo) RecordScanSeen(ctx context.Context, scanID, fileID string, resourceKey *string) error {
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO scan_seen (scan_id, file_id, resource_key)
			VALUES (?, ?, ?)
			ON CONFLICT(scan_id, file_id) DO UPDATE SET resource_key = excluded.resource_key;
		`, scanID, fileID, resourceKey)
		return err
	})
}
