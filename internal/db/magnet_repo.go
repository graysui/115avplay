package db

import (
	"context"
	"database/sql"
	"fmt"

	"mediavault/internal/models"
)

type MagnetRepo struct {
	db *DB
}

func NewMagnetRepo(db *DB) *MagnetRepo {
	return &MagnetRepo{db: db}
}

// ResolvedSource contains the result of default source resolution for a movie.
type ResolvedSource struct {
	Tier       int            // 1: permanent_ready, 2: temporary_ready, 3: transferable
	Magnet     *models.Magnet
	CloudAsset *models.CloudAsset
}

func scanMagnet(scanner rowScanner) (*models.Magnet, error) {
	var m models.Magnet
	var contentHash, title, section, website, pubDate sql.NullString

	err := scanner.Scan(
		&m.InfoHash, &m.MovieCode, &m.ResourceKind, &contentHash, &m.MagnetURL, &title,
		&m.SizeBytes, &section, &m.QualityLabel, &website, &pubDate, &m.HasChineseSub,
		&m.IsCracked, &m.Is4K, &m.IsCensored, &m.PriorityScore, &m.IsPreferred, &m.Enabled,
		&m.MetadataSources, &m.ManualFields, &m.LegacyRuntime, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if contentHash.Valid {
		m.ContentHash = &contentHash.String
	}
	if title.Valid {
		m.Title = &title.String
	}
	if section.Valid {
		m.Section = &section.String
	}
	if website.Valid {
		m.Website = &website.String
	}
	if pubDate.Valid {
		m.PublishDate = &pubDate.String
	}

	return &m, nil
}

// ResolveDefaultSource performs real-time tier-based source resolution according to design §7.1.
func (r *MagnetRepo) ResolveDefaultSource(ctx context.Context, movieCode, now string) (*ResolvedSource, error) {
	query := `
		SELECT m.info_hash, m.movie_code, m.resource_kind, m.content_hash, m.magnet_url, m.title,
		       m.size_bytes, m.section, m.quality_label, m.website, m.publish_date, m.has_chinese_sub,
		       m.is_cracked, m.is_4k, m.is_censored, m.priority_score, m.is_preferred, m.enabled,
		       m.metadata_sources, m.manual_fields, m.legacy_runtime, m.created_at, m.updated_at,
		       a.id, a.binding_id, a.resource_key, a.owning_job_id, a.source_type, a.state,
		       a.generation, a.file_id, a.pick_code, a.file_name, a.size_bytes, a.container,
		       a.runtime_ticks, a.media_streams, a.parent_id, a.owned_root_id, a.root_snapshot,
		       a.manifest_json, a.ready_at, a.expires_at, a.deleted_at, a.last_seen_at, a.last_error,
		       a.created_at, a.updated_at,
		       CASE
		         WHEN a.id IS NOT NULL AND a.source_type = 'permanent' AND a.state = 'ready' AND a.expires_at IS NULL THEN 1
		         WHEN a.id IS NOT NULL AND a.source_type = 'temporary' AND a.state = 'ready' AND a.expires_at > ? THEN 2
		         WHEN m.resource_kind IN ('btih', 'ed2k') THEN 3
		         ELSE 4
		       END as tier
		FROM offline_magnets m
		LEFT JOIN cloud_assets a ON a.resource_key = m.info_hash
		     AND a.state = 'ready'
		     AND ( (a.source_type = 'permanent' AND a.expires_at IS NULL)
		        OR (a.source_type = 'temporary' AND a.expires_at > ?) )
		WHERE m.movie_code = ? AND m.enabled = 1
		ORDER BY tier ASC, m.priority_score DESC, m.size_bytes DESC, m.info_hash ASC
		LIMIT 1
	`

	var res ResolvedSource
	var m models.Magnet
	var contentHash, mTitle, section, website, pubDate sql.NullString

	var a models.CloudAsset
	var assetID, bindingID, resKey, owningJobID, srcType, state, fileID, pickCode, fileName sql.NullString
	var container, mediaStreams, parentID, ownedRootID, rootSnapshot, manifestJSON sql.NullString
	var readyAt, expiresAt, deletedAt, lastSeenAt, lastError, aCreatedAt, aUpdatedAt sql.NullString
	var aSizeBytes sql.NullInt64
	var aRuntimeTicks sql.NullInt64
	var aGeneration sql.NullInt64
	var tier int

	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		row := database.QueryRowContext(ctx, query, now, now, movieCode)
		return row.Scan(
			&m.InfoHash, &m.MovieCode, &m.ResourceKind, &contentHash, &m.MagnetURL, &mTitle,
			&m.SizeBytes, &section, &m.QualityLabel, &website, &pubDate, &m.HasChineseSub,
			&m.IsCracked, &m.Is4K, &m.IsCensored, &m.PriorityScore, &m.IsPreferred, &m.Enabled,
			&m.MetadataSources, &m.ManualFields, &m.LegacyRuntime, &m.CreatedAt, &m.UpdatedAt,
			&assetID, &bindingID, &resKey, &owningJobID, &srcType, &state,
			&aGeneration, &fileID, &pickCode, &fileName, &aSizeBytes, &container,
			&aRuntimeTicks, &mediaStreams, &parentID, &ownedRootID, &rootSnapshot,
			&manifestJSON, &readyAt, &expiresAt, &deletedAt, &lastSeenAt, &lastError,
			&aCreatedAt, &aUpdatedAt,
			&tier,
		)
	})

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // No candidates found
		}
		return nil, fmt.Errorf("resolve default source: %w", err)
	}

	if tier > 3 {
		return nil, nil // Candidates exist but none is playable or transferable
	}

	if contentHash.Valid {
		m.ContentHash = &contentHash.String
	}
	if mTitle.Valid {
		m.Title = &mTitle.String
	}
	if section.Valid {
		m.Section = &section.String
	}
	if website.Valid {
		m.Website = &website.String
	}
	if pubDate.Valid {
		m.PublishDate = &pubDate.String
	}

	res.Tier = tier
	res.Magnet = &m

	if assetID.Valid && assetID.String != "" {
		a.ID = assetID.String
		if bindingID.Valid {
			a.BindingID = bindingID.String
		}
		if resKey.Valid {
			a.ResourceKey = &resKey.String
		}
		if owningJobID.Valid {
			a.OwningJobID = &owningJobID.String
		}
		if srcType.Valid {
			a.SourceType = srcType.String
		}
		if state.Valid {
			a.State = state.String
		}
		if aGeneration.Valid {
			a.Generation = int(aGeneration.Int64)
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
		if aSizeBytes.Valid {
			a.SizeBytes = aSizeBytes.Int64
		}
		if container.Valid {
			a.Container = &container.String
		}
		if aRuntimeTicks.Valid {
			a.RuntimeTicks = &aRuntimeTicks.Int64
		}
		if mediaStreams.Valid {
			a.MediaStreams = &mediaStreams.String
		}
		if parentID.Valid {
			a.ParentID = &parentID.String
		}
		if ownedRootID.Valid {
			a.OwnedRootID = &ownedRootID.String
		}
		if rootSnapshot.Valid {
			a.RootSnapshot = &rootSnapshot.String
		}
		if manifestJSON.Valid {
			a.ManifestJSON = manifestJSON.String
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
		if lastError.Valid {
			a.LastError = &lastError.String
		}
		if aCreatedAt.Valid {
			a.CreatedAt = aCreatedAt.String
		}
		if aUpdatedAt.Valid {
			a.UpdatedAt = aUpdatedAt.String
		}
		res.CloudAsset = &a
	}

	return &res, nil
}

// ListMagnetsByMovie lists all magnets for a movie ordered by priority_score DESC, size_bytes DESC, info_hash ASC.
func (r *MagnetRepo) ListMagnetsByMovie(ctx context.Context, movieCode string) ([]models.Magnet, error) {
	query := `
		SELECT info_hash, movie_code, resource_kind, content_hash, magnet_url, title,
		       size_bytes, section, quality_label, website, publish_date, has_chinese_sub,
		       is_cracked, is_4k, is_censored, priority_score, is_preferred, enabled,
		       metadata_sources, manual_fields, legacy_runtime, created_at, updated_at
		FROM offline_magnets
		WHERE movie_code = ?
		ORDER BY priority_score DESC, size_bytes DESC, info_hash ASC
	`

	var list []models.Magnet
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		rows, err := database.QueryContext(ctx, query, movieCode)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			m, err := scanMagnet(rows)
			if err != nil {
				return err
			}
			list = append(list, *m)
		}
		return nil
	})

	return list, err
}

// RecomputePreferredMagnetTx recomputes and sets the preferred magnet for a movie within an existing transaction.
func RecomputePreferredMagnetTx(ctx context.Context, tx *sql.Tx, movieCode string) error {
	now := models.UTCNow()
	// Clear current preferred
	if _, err := tx.ExecContext(ctx, `
		UPDATE offline_magnets SET is_preferred = 0, updated_at = ? WHERE movie_code = ? AND is_preferred = 1
	`, now, movieCode); err != nil {
		return err
	}

	// Real-time tier selection:
	// Tier 1: permanent ready asset
	// Tier 2: temporary ready asset (expires_at > now)
	// Tier 3: transferable enabled magnet
	bestQuery := `
		SELECT m.info_hash
		FROM offline_magnets m
		LEFT JOIN cloud_assets a ON a.resource_key = m.info_hash AND a.state = 'ready'
		WHERE m.movie_code = ? AND m.enabled = 1
		ORDER BY
			CASE
				WHEN a.source_type = 'permanent' THEN 1
				WHEN a.source_type = 'temporary' AND a.expires_at > ? THEN 2
				WHEN m.resource_kind IN ('btih', 'ed2k') THEN 3
				ELSE 4
			END ASC,
			m.priority_score DESC,
			m.size_bytes DESC,
			m.info_hash ASC
		LIMIT 1;
	`
	var bestHash string
	err := tx.QueryRowContext(ctx, bestQuery, movieCode, now).Scan(&bestHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil // No enabled candidate
		}
		return err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE offline_magnets SET is_preferred = 1, updated_at = ? WHERE info_hash = ? AND movie_code = ?
	`, now, bestHash, movieCode)
	return err
}

// SetPreferredMagnet updates the preferred magnet for a movie within a single transaction.
func (r *MagnetRepo) SetPreferredMagnet(ctx context.Context, movieCode, infoHash string) error {
	now := models.UTCNow()
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		// Clear existing preferred
		if _, err := tx.ExecContext(ctx, `
			UPDATE offline_magnets SET is_preferred = 0, updated_at = ? WHERE movie_code = ? AND is_preferred = 1
		`, now, movieCode); err != nil {
			return err
		}

		// Set new preferred
		res, err := tx.ExecContext(ctx, `
			UPDATE offline_magnets SET is_preferred = 1, updated_at = ? WHERE info_hash = ? AND movie_code = ?
		`, now, infoHash, movieCode)
		if err != nil {
			return err
		}
		rowsAffected, _ := res.RowsAffected()
		if rowsAffected == 0 {
			return fmt.Errorf("magnet %s not found for movie %s", infoHash, movieCode)
		}
		return nil
	})
}

// UpsertMagnet inserts or updates a magnet record. Note: priority_score is a generated column.
func (r *MagnetRepo) UpsertMagnet(ctx context.Context, m *models.Magnet) error {
	now := models.UTCNow()
	if m.CreatedAt == "" {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	if m.MetadataSources == "" {
		m.MetadataSources = "{}"
	}
	if m.ManualFields == "" {
		m.ManualFields = "[]"
	}
	if m.LegacyRuntime == "" {
		m.LegacyRuntime = "{}"
	}

	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO offline_magnets (
				info_hash, movie_code, resource_kind, content_hash, magnet_url, title,
				size_bytes, section, quality_label, website, publish_date, has_chinese_sub,
				is_cracked, is_4k, is_censored, is_preferred, enabled,
				metadata_sources, manual_fields, legacy_runtime, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(info_hash) DO UPDATE SET
				title = COALESCE(excluded.title, offline_magnets.title),
				size_bytes = CASE WHEN excluded.size_bytes > 0 THEN excluded.size_bytes ELSE offline_magnets.size_bytes END,
				quality_label = excluded.quality_label,
				has_chinese_sub = excluded.has_chinese_sub,
				is_cracked = excluded.is_cracked,
				is_4k = excluded.is_4k,
				is_censored = excluded.is_censored,
				enabled = excluded.enabled,
				metadata_sources = excluded.metadata_sources,
				manual_fields = excluded.manual_fields,
				updated_at = excluded.updated_at
		`,
			m.InfoHash, m.MovieCode, m.ResourceKind, m.ContentHash, m.MagnetURL, m.Title,
			m.SizeBytes, m.Section, m.QualityLabel, m.Website, m.PublishDate, m.HasChineseSub,
			m.IsCracked, m.Is4K, m.IsCensored, m.IsPreferred, m.Enabled,
			m.MetadataSources, m.ManualFields, m.LegacyRuntime, m.CreatedAt, m.UpdatedAt,
		)
		return err
	})
}
