package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"mediavault/internal/db"
	"mediavault/internal/models"
	"strings"
)

type ConflictRecord struct {
	InfoHash     string `json:"info_hash"`
	ExistingCode string `json:"existing_code"`
	IncomingCode string `json:"incoming_code"`
	IncomingURL  string `json:"incoming_url"`
}

type BatchStats struct {
	TotalProcessed  int              `json:"total_processed"`
	MoviesInserted  int              `json:"movies_inserted"`
	MoviesUpdated   int              `json:"movies_updated"`
	MagnetsInserted int              `json:"magnets_inserted"`
	MagnetsUpdated  int              `json:"magnets_updated"`
	Conflicts       []ConflictRecord `json:"conflicts,omitempty"`
}

// MergeBatchTx executes a single 1000-row batch merge inside a database transaction.
func MergeBatchTx(ctx context.Context, tx *sql.Tx, records []*IngestRecord) (*BatchStats, error) {
	stats := &BatchStats{
		TotalProcessed: len(records),
	}

	now := models.UTCNow()
	affectedMovies := make(map[string]bool)

	for _, rec := range records {
		if rec == nil {
			continue
		}

		// 1. Process Movie
		movieInserted, err := mergeMovie(ctx, tx, rec, now)
		if err != nil {
			return nil, fmt.Errorf("merge movie %s: %w", rec.Code, err)
		}
		if movieInserted {
			stats.MoviesInserted++
		} else {
			stats.MoviesUpdated++
		}
		affectedMovies[rec.Code] = true

		// 2. Process Magnet / Resource
		magInserted, conflict, err := mergeMagnet(ctx, tx, rec, now)
		if err != nil {
			return nil, fmt.Errorf("merge magnet for %s: %w", rec.Code, err)
		}
		if conflict != nil {
			stats.Conflicts = append(stats.Conflicts, *conflict)
		} else if magInserted {
			stats.MagnetsInserted++
		} else {
			stats.MagnetsUpdated++
		}
	}

	// 3. Recompute preferred magnet for all affected movies in this batch
	for code := range affectedMovies {
		if err := db.RecomputePreferredMagnetTx(ctx, tx, code); err != nil {
			return nil, fmt.Errorf("recompute preferred magnet for %s: %w", code, err)
		}
	}

	return stats, nil
}

func mergeMovie(ctx context.Context, tx *sql.Tx, rec *IngestRecord, now string) (inserted bool, err error) {
	// Check if movie already exists
	queryCheck := `SELECT code, category, source_websites, manual_fields, scrape_policy FROM offline_movies WHERE code = ?;`
	row := tx.QueryRowContext(ctx, queryCheck, rec.Code)

	var existingCode, existingCategory, existingWebsitesJSON, manualFieldsJSON, existingScrapePolicy string
	err = row.Scan(&existingCode, &existingCategory, &existingWebsitesJSON, &manualFieldsJSON, &existingScrapePolicy)

	if err == sql.ErrNoRows {
		// New Movie Insertion
		websites := []string{}
		if rec.SourceWebsite != "" {
			websites = append(websites, rec.SourceWebsite)
		}
		webJSON, _ := json.Marshal(websites)

		var previewStr *string
		if len(rec.PreviewImages) > 0 {
			p := strings.Join(rec.PreviewImages, ",")
			previewStr = &p
		}

		insertQuery := `
			INSERT INTO offline_movies (
				code, title, category, publish_date, first_seen_at, preview_images,
				source_websites, actors, tags, is_enriched, scrape_policy, policy_reason,
				scrape_status, not_found_count, partial_attempts, metadata_sources,
				manual_fields, legacy_metadata, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, '[]', '[]', ?, ?, ?, 'idle', 0, 0, '{}', '[]', '{}', ?, ?);
		`
		_, err = tx.ExecContext(ctx, insertQuery,
			rec.Code, rec.Title, rec.Category, rec.PublishDate, now, previewStr,
			string(webJSON), rec.IsEnriched, rec.ScrapePolicy, rec.PolicyReason, now, now,
		)
		if err != nil {
			return false, err
		}
		return true, nil
	} else if err != nil {
		return false, err
	}

	// Existing movie: parse manual locks
	var manualFields []string
	_ = json.Unmarshal([]byte(manualFieldsJSON), &manualFields)
	isManualLocked := func(field string) bool {
		for _, f := range manualFields {
			if f == field {
				return true
			}
		}
		return false
	}

	// Merge source websites
	var websites []string
	_ = json.Unmarshal([]byte(existingWebsitesJSON), &websites)
	hasWeb := false
	for _, w := range websites {
		if w == rec.SourceWebsite {
			hasWeb = true
			break
		}
	}
	if !hasWeb && rec.SourceWebsite != "" {
		websites = append(websites, rec.SourceWebsite)
	}
	newWebJSON, _ := json.Marshal(websites)

	// Controlled updates
	updateQuery := `
		UPDATE offline_movies
		SET source_websites = ?,
		    category = CASE WHEN ? AND category = '未知' THEN ? ELSE category END,
		    publish_date = CASE WHEN ? AND (publish_date IS NULL OR publish_date = '') THEN ? ELSE publish_date END,
		    scrape_policy = CASE WHEN ? AND ? = 'exempt' THEN 'exempt' ELSE scrape_policy END,
		    policy_reason = CASE WHEN ? AND ? = 'exempt' THEN 'category' ELSE policy_reason END,
		    updated_at = ?
		WHERE code = ?;
	`
	canUpdateCategory := !isManualLocked("category")
	canUpdatePublishDate := !isManualLocked("publish_date")
	canUpdateScrapePolicy := !isManualLocked("scrape_policy")

	_, err = tx.ExecContext(ctx, updateQuery,
		string(newWebJSON),
		canUpdateCategory, rec.Category,
		canUpdatePublishDate, rec.PublishDate,
		canUpdateScrapePolicy, rec.ScrapePolicy,
		canUpdateScrapePolicy, rec.ScrapePolicy,
		now, rec.Code,
	)
	if err != nil {
		return false, err
	}
	return false, nil
}

func mergeMagnet(ctx context.Context, tx *sql.Tx, rec *IngestRecord, now string) (inserted bool, conflict *ConflictRecord, err error) {
	infoHash := rec.ResourceKey

	// Check if magnet already exists
	query := `SELECT info_hash, movie_code, size_bytes, manual_fields FROM offline_magnets WHERE info_hash = ?;`
	row := tx.QueryRowContext(ctx, query, infoHash)

	var existingHash, existingMovieCode, manualFieldsJSON string
	var existingSizeBytes int64
	err = row.Scan(&existingHash, &existingMovieCode, &existingSizeBytes, &manualFieldsJSON)

	if err == sql.ErrNoRows {
		// New Magnet Insertion
		insertQuery := `
			INSERT INTO offline_magnets (
				info_hash, movie_code, resource_kind, content_hash, magnet_url,
				title, size_bytes, section, quality_label, website, publish_date,
				has_chinese_sub, is_cracked, is_4k, is_censored, is_preferred,
				enabled, metadata_sources, manual_fields, legacy_runtime, created_at, updated_at
			) VALUES (?, ?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 1, '{}', '[]', '{}', ?, ?);
		`
		_, err = tx.ExecContext(ctx, insertQuery,
			infoHash, rec.Code, rec.ResourceKind, rec.MagnetURL,
			rec.Title, rec.SizeBytes, rec.Section, rec.QualityLabel, rec.SourceWebsite,
			rec.PublishDate, rec.Quality.HasChineseSub, rec.Quality.IsCracked, rec.Quality.Is4K, rec.Quality.IsCensored, now, now,
		)
		if err != nil {
			return false, nil, err
		}
		return true, nil, nil
	} else if err != nil {
		return false, nil, err
	}

	// If info_hash already exists, check movie_code
	if existingMovieCode != rec.Code {
		// Cross-movie conflict! Keep existing movie_code, do NOT reassign.
		return false, &ConflictRecord{
			InfoHash:     infoHash,
			ExistingCode: existingMovieCode,
			IncomingCode: rec.Code,
			IncomingURL:  rec.MagnetURL,
		}, nil
	}

	// Same movie: update fields if not manually locked
	var manualFields []string
	_ = json.Unmarshal([]byte(manualFieldsJSON), &manualFields)
	isManualLocked := func(field string) bool {
		for _, f := range manualFields {
			if f == field {
				return true
			}
		}
		return false
	}

	updateQuery := `
		UPDATE offline_magnets
		SET title = CASE WHEN ? AND (title IS NULL OR title = '') THEN ? ELSE title END,
		    size_bytes = CASE WHEN ? AND size_bytes = 0 AND ? > 0 THEN ? ELSE size_bytes END,
		    section = CASE WHEN ? AND (section IS NULL OR section = '') THEN ? ELSE section END,
		    website = CASE WHEN ? AND (website IS NULL OR website = '') THEN ? ELSE website END,
		    publish_date = CASE WHEN ? AND (publish_date IS NULL OR publish_date = '') THEN ? ELSE publish_date END,
		    updated_at = ?
		WHERE info_hash = ?;
	`
	_, err = tx.ExecContext(ctx, updateQuery,
		!isManualLocked("title"), rec.Title,
		!isManualLocked("size_bytes"), rec.SizeBytes, rec.SizeBytes,
		!isManualLocked("section"), rec.Section,
		!isManualLocked("website"), rec.SourceWebsite,
		!isManualLocked("publish_date"), rec.PublishDate,
		now, infoHash,
	)
	if err != nil {
		return false, nil, err
	}

	return false, nil, nil
}
