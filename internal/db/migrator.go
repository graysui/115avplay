package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"mediavault/internal/identity"

	"github.com/google/uuid"
)

// Migrator handles database schema migrations and legacy upgrades.
type Migrator struct {
	db *DB
}

func NewMigrator(db *DB) *Migrator {
	return &Migrator{db: db}
}

// BackupDatabase copies the database file to a timestamped backup before migration.
func (m *Migrator) BackupDatabase(sourcePath string) (string, error) {
	timestamp := time.Now().UTC().Format("20060102-150405")
	backupPath := fmt.Sprintf("%s.bak-%s", sourcePath, timestamp)

	src, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("open source database for backup: %w", err)
	}
	defer src.Close()

	dst, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("create backup file: %w", err)
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("copy database backup: %w", err)
	}

	return backupPath, nil
}

// MigrateLegacyV0 executes the complete migration from legacy-v0 to schema_version=1 inside a single transaction.
func (m *Migrator) MigrateLegacyV0(ctx context.Context) (*SchemaMetaInfo, error) {
	// 1. Backup original DB
	backupPath, err := m.BackupDatabase(m.db.Path())
	if err != nil {
		return nil, fmt.Errorf("pre-migration backup failed: %w", err)
	}

	migrationAt := time.Now().UTC().Format(time.RFC3339)
	serverID := uuid.New().String()
	checksum := SchemaChecksum()

	// 2. Perform migration using exclusive writer
	err = m.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		// Disable foreign keys during table recreation
		if _, err := tx.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return fmt.Errorf("disable foreign_keys: %w", err)
		}

		// A. Rename old tables to temporary staging tables
		if _, err := tx.ExecContext(ctx, `
			ALTER TABLE offline_movies RENAME TO _legacy_movies;
			ALTER TABLE offline_magnets RENAME TO _legacy_magnets;
		`); err != nil {
			return fmt.Errorf("rename legacy tables: %w", err)
		}

		// B. Apply TargetSchemaDDL
		statements := splitSQLStatements(TargetSchemaDDL)
		for _, stmt := range statements {
			trimmed := strings.TrimSpace(stmt)
			if trimmed == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, trimmed); err != nil {
				return fmt.Errorf("execute target DDL [%s...]: %w", truncate(trimmed, 40), err)
			}
		}

		// C. Migrate offline_movies in batches
		if err := m.migrateMoviesTable(ctx, tx, migrationAt); err != nil {
			return fmt.Errorf("migrate movies: %w", err)
		}

		// D. Migrate offline_magnets in batches
		if err := m.migrateMagnetsTable(ctx, tx, migrationAt); err != nil {
			return fmt.Errorf("migrate magnets: %w", err)
		}

		// E. Drop temporary legacy tables
		if _, err := tx.ExecContext(ctx, `
			DROP TABLE _legacy_movies;
			DROP TABLE _legacy_magnets;
		`); err != nil {
			return fmt.Errorf("drop legacy tables: %w", err)
		}

		// F. Record schema metadata
		insertMeta := `INSERT OR REPLACE INTO schema_meta (key, value) VALUES (?, ?)`
		metas := [][2]string{
			{"schema_version", strconv.Itoa(CurrentSchemaVersion)},
			{"server_id", serverID},
			{"migration_at", migrationAt},
			{"migration_checksum", checksum},
		}
		for _, item := range metas {
			if _, err := tx.ExecContext(ctx, insertMeta, item[0], item[1]); err != nil {
				return fmt.Errorf("record metadata %s: %w", item[0], err)
			}
		}

		// G. Verify constraints: foreign_key_check and quick_check
		var fkCount int
		rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return fmt.Errorf("foreign_key_check: %w", err)
		}
		for rows.Next() {
			fkCount++
		}
		_ = rows.Close()
		if fkCount > 0 {
			return fmt.Errorf("foreign_key_check detected %d violations", fkCount)
		}

		var qc string
		if err := tx.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&qc); err != nil || qc != "ok" {
			return fmt.Errorf("quick_check failed: %s (err: %v)", qc, err)
		}

		// Re-enable foreign keys
		if _, err := tx.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
			return fmt.Errorf("re-enable foreign_keys: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("migration failed (backup retained at %s): %w", backupPath, err)
	}

	return &SchemaMetaInfo{
		Version:    CurrentSchemaVersion,
		ServerID:   serverID,
		Checksum:   checksum,
		MigratedAt: migrationAt,
	}, nil
}

func (m *Migrator) migrateMoviesTable(ctx context.Context, tx *sql.Tx, migrationAt string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT code, title, category, publish_date, preview_images, source_websites,
		       title_zh, description_zh, cover_url, poster_url, actors, tags,
		       maker, director, score, is_enriched, scrape_status, scrape_failed_reason,
		       scrape_retry_count, last_scraped_at
		FROM _legacy_movies
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO offline_movies (
			code, title, official_title, category, publish_date, release_date, first_seen_at,
			preview_images, source_websites, title_zh, description_zh, cover_url, poster_url,
			actors, tags, maker, director, score, runtime_ticks, is_enriched, scrape_policy,
			policy_reason, scrape_status, scrape_failed_reason, not_found_count, last_not_found_day,
			partial_attempts, next_scrape_at, last_scraped_at, metadata_sources, manual_fields,
			legacy_metadata, deleted_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for rows.Next() {
		var code string
		var title, category, pubDate, previewImages, sourceWebsites sql.NullString
		var titleZh, descZh, coverURL, posterURL, actors, tags sql.NullString
		var maker, director sql.NullString
		var score sql.NullFloat64
		var isEnriched, scrapeStatus, retryCount sql.NullInt64
		var scrapeFailedReason sql.NullString
		var lastScrapedAt sql.NullString

		if err := rows.Scan(
			&code, &title, &category, &pubDate, &previewImages, &sourceWebsites,
			&titleZh, &descZh, &coverURL, &posterURL, &actors, &tags,
			&maker, &director, &score, &isEnriched, &scrapeStatus, &scrapeFailedReason,
			&retryCount, &lastScrapedAt,
		); err != nil {
			return err
		}

		// 1. Category mapping
		catStr := "未知"
		legacyMetaMap := make(map[string]interface{})
		if category.Valid && strings.TrimSpace(category.String) != "" {
			catStr = strings.TrimSpace(category.String)
		} else {
			legacyMetaMap["original_category"] = category.String
		}

		// 2. Source websites: comma-delimited string to JSON array
		sourceWebsitesJSON := "[]"
		if sourceWebsites.Valid && strings.TrimSpace(sourceWebsites.String) != "" {
			sites := strings.Split(sourceWebsites.String, ",")
			var cleanSites []string
			seenSites := make(map[string]struct{})
			for _, s := range sites {
				trimmed := strings.TrimSpace(s)
				if trimmed != "" {
					if _, ok := seenSites[trimmed]; !ok {
						seenSites[trimmed] = struct{}{}
						cleanSites = append(cleanSites, trimmed)
					}
				}
			}
			if b, err := json.Marshal(cleanSites); err == nil {
				sourceWebsitesJSON = string(b)
			}
			legacyMetaMap["original_source_websites"] = sourceWebsites.String
		}

		// 3. Actors & Tags JSON validation
		actorsJSON := "[]"
		if actors.Valid && isValidJSONArray(actors.String) {
			actorsJSON = actors.String
		} else if actors.Valid && actors.String != "" {
			legacyMetaMap["original_actors"] = actors.String
		}

		tagsJSON := "[]"
		if tags.Valid && isValidJSONArray(tags.String) {
			tagsJSON = tags.String
		} else if tags.Valid && tags.String != "" {
			legacyMetaMap["original_tags"] = tags.String
		}

		// 4. Score validation (0 ~ 5)
		var validScore *float64
		if score.Valid {
			if score.Float64 >= 0 && score.Float64 <= 5 {
				s := score.Float64
				validScore = &s
			} else {
				legacyMetaMap["original_score"] = score.Float64
			}
		}

		// 5. is_enriched & scrape_policy & scrape_status mapping (docs/DATABASE_MIGRATION.md §3)
		oldEnriched := int64(0)
		if isEnriched.Valid {
			oldEnriched = isEnriched.Int64
		}

		targetEnriched := 0
		targetPolicy := "auto"
		var policyReason *string
		targetStatus := "idle"

		hasTitle := (titleZh.Valid && titleZh.String != "") || (title.Valid && title.String != "")
		hasDesc := descZh.Valid && descZh.String != ""
		hasCover := coverURL.Valid && coverURL.String != ""

		if hasTitle && hasDesc && hasCover {
			targetEnriched = 1
		} else if hasTitle || hasDesc || hasCover {
			targetEnriched = 3
		}

		switch oldEnriched {
		case 1:
			// If missing fields, convert to 3 and auto
			if targetEnriched != 1 {
				targetEnriched = 3
				targetPolicy = "auto"
			}
		case 2:
			// Old 2 represented exempt/skip
			if strings.Contains(catStr, "FC2") || strings.Contains(catStr, "国产") || strings.Contains(catStr, "麻豆") {
				targetPolicy = "exempt"
				reason := "category"
				policyReason = &reason
			} else {
				targetPolicy = "paused"
				reason := "legacy_skip"
				policyReason = &reason
			}
		default: // 0
			targetPolicy = "auto"
		}

		// Save old scrape details
		if retryCount.Valid && retryCount.Int64 > 0 {
			legacyMetaMap["original_retry_count"] = retryCount.Int64
		}
		if scrapeFailedReason.Valid && scrapeFailedReason.String != "" {
			legacyMetaMap["original_failed_reason"] = scrapeFailedReason.String
		}

		legacyMetaBytes, _ := json.Marshal(legacyMetaMap)

		var pubDateVal *string
		if pubDate.Valid && pubDate.String != "" {
			pubDateVal = &pubDate.String
		}

		var titleVal *string
		if title.Valid {
			titleVal = &title.String
		}

		var titleZhVal, descZhVal, coverURLVal, posterURLVal, makerVal, directorVal, previewVal *string
		if titleZh.Valid {
			titleZhVal = &titleZh.String
		}
		if descZh.Valid {
			descZhVal = &descZh.String
		}
		if coverURL.Valid {
			coverURLVal = &coverURL.String
		}
		if posterURL.Valid {
			posterURLVal = &posterURL.String
		}
		if maker.Valid {
			makerVal = &maker.String
		}
		if director.Valid {
			directorVal = &director.String
		}
		if previewImages.Valid {
			previewVal = &previewImages.String
		}

		var lastScrapedVal *string
		if lastScrapedAt.Valid && lastScrapedAt.String != "" {
			lastScrapedVal = &lastScrapedAt.String
		}

		_, err := stmt.ExecContext(ctx,
			code, titleVal, nil, catStr, pubDateVal, nil, migrationAt,
			previewVal, sourceWebsitesJSON, titleZhVal, descZhVal, coverURLVal, posterURLVal,
			actorsJSON, tagsJSON, makerVal, directorVal, validScore, nil, targetEnriched, targetPolicy,
			policyReason, targetStatus, nil, 0, nil,
			0, nil, lastScrapedVal, "{}", "[]",
			string(legacyMetaBytes), nil, migrationAt, migrationAt,
		)
		if err != nil {
			return fmt.Errorf("insert movie %s: %w", code, err)
		}
	}

	return nil
}

func (m *Migrator) migrateMagnetsTable(ctx context.Context, tx *sql.Tx, migrationAt string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT info_hash, movie_code, magnet_url, title, size_mb, section,
		       quality_label, website, publish_date, priority_score, is_preferred,
		       source_type, storage_cid, target_file_id, target_pick_code, target_file_name,
		       target_file_size, target_container, transfer_status, is_available, temp_expire_at, last_error
		FROM _legacy_magnets
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO offline_magnets (
			info_hash, movie_code, resource_kind, content_hash, magnet_url, title,
			size_bytes, section, quality_label, website, publish_date, has_chinese_sub,
			is_cracked, is_4k, is_censored, is_preferred, enabled,
			metadata_sources, manual_fields, legacy_runtime, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for rows.Next() {
		var infoHash, movieCode, magnetURL string
		var title, section, qualityLabel, website, pubDate sql.NullString
		var sizeMB, priorityScore, targetFileSize, transferStatus sql.NullInt64
		var isPreferred, isAvailable sql.NullBool
		var sourceType, storageCID, targetFileID, targetPickCode, targetFileName, targetContainer, tempExpireAt, lastError sql.NullString

		if err := rows.Scan(
			&infoHash, &movieCode, &magnetURL, &title, &sizeMB, &section,
			&qualityLabel, &website, &pubDate, &priorityScore, &isPreferred,
			&sourceType, &storageCID, &targetFileID, &targetPickCode, &targetFileName,
			&targetFileSize, &targetContainer, &transferStatus, &isAvailable, &tempExpireAt, &lastError,
		); err != nil {
			return err
		}

		// Calculate size_bytes
		var sizeBytes int64 = 0
		if targetFileSize.Valid && targetFileSize.Int64 > 0 {
			sizeBytes = targetFileSize.Int64
		} else if sizeMB.Valid && sizeMB.Int64 > 0 {
			sizeBytes = sizeMB.Int64 * 1048576
		}

		// Re-extract quality flags from title using pure function
		titleStr := ""
		if title.Valid {
			titleStr = title.String
		}
		flags := identity.ExtractQualityFlags(titleStr)

		// Store legacy runtime fields in JSON
		legacyRuntimeMap := make(map[string]interface{})
		if sizeMB.Valid {
			legacyRuntimeMap["size_mb"] = sizeMB.Int64
		}
		if sourceType.Valid {
			legacyRuntimeMap["source_type"] = sourceType.String
		}
		if storageCID.Valid {
			legacyRuntimeMap["storage_cid"] = storageCID.String
		}
		if targetFileID.Valid {
			legacyRuntimeMap["target_file_id"] = targetFileID.String
		}
		if targetPickCode.Valid {
			legacyRuntimeMap["target_pick_code"] = targetPickCode.String
		}
		if targetFileName.Valid {
			legacyRuntimeMap["target_file_name"] = targetFileName.String
		}
		if targetFileSize.Valid {
			legacyRuntimeMap["target_file_size"] = targetFileSize.Int64
		}
		if targetContainer.Valid {
			legacyRuntimeMap["target_container"] = targetContainer.String
		}
		if transferStatus.Valid {
			legacyRuntimeMap["transfer_status"] = transferStatus.Int64
		}
		if isAvailable.Valid {
			legacyRuntimeMap["is_available"] = isAvailable.Bool
		}
		if tempExpireAt.Valid {
			legacyRuntimeMap["temp_expire_at"] = tempExpireAt.String
		}
		if lastError.Valid {
			legacyRuntimeMap["last_error"] = lastError.String
		}
		if priorityScore.Valid {
			legacyRuntimeMap["old_priority_score"] = priorityScore.Int64
		}

		legacyRuntimeBytes, _ := json.Marshal(legacyRuntimeMap)

		qlStr := "unknown"
		if qualityLabel.Valid && qualityLabel.String != "" {
			qlStr = qualityLabel.String
		}

		var titleVal, sectionVal, websiteVal, pubDateVal *string
		if title.Valid {
			titleVal = &title.String
		}
		if section.Valid {
			sectionVal = &section.String
		}
		if website.Valid {
			websiteVal = &website.String
		}
		if pubDate.Valid {
			pubDateVal = &pubDate.String
		}

		_, err := stmt.ExecContext(ctx,
			infoHash, movieCode, "btih", infoHash, magnetURL, titleVal,
			sizeBytes, sectionVal, qlStr, websiteVal, pubDateVal, flags.HasChineseSub,
			flags.IsCracked, flags.Is4K, flags.IsCensored, 0, 1, // preferred=0
			"{}", "[]", string(legacyRuntimeBytes), migrationAt, migrationAt,
		)
		if err != nil {
			return fmt.Errorf("insert magnet %s: %w", infoHash, err)
		}
	}

	return nil
}

func isValidJSONArray(str string) bool {
	trimmed := strings.TrimSpace(str)
	if !strings.HasPrefix(trimmed, "[") || !strings.HasSuffix(trimmed, "]") {
		return false
	}
	var arr []interface{}
	return json.Unmarshal([]byte(trimmed), &arr) == nil
}
