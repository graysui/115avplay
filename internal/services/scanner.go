package services

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"mediavault/internal/client115"
	"mediavault/internal/db"
	"mediavault/internal/identity"
	"mediavault/internal/models"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

type ScanStats struct {
	TotalFilesSeen int
	ValidVideos    int
	MatchedMovies  int
	AssetsCreated  int
}

type TreeScanner struct {
	database   *db.DB
	assetRepo  *db.AssetRepo
	movieRepo  *db.MovieRepo
	magnetRepo *db.MagnetRepo
	jobRepo    *db.JobRepo
	c115Client *client115.Client
	logger     *slog.Logger
}

func NewTreeScanner(
	database *db.DB,
	assetRepo *db.AssetRepo,
	movieRepo *db.MovieRepo,
	magnetRepo *db.MagnetRepo,
	jobRepo *db.JobRepo,
	c115Client *client115.Client,
	logger *slog.Logger,
) *TreeScanner {
	if logger == nil {
		logger = slog.Default()
	}
	return &TreeScanner{
		database:   database,
		assetRepo:  assetRepo,
		movieRepo:  movieRepo,
		magnetRepo: magnetRepo,
		jobRepo:    jobRepo,
		c115Client: c115Client,
		logger:     logger,
	}
}

// ScanCID scans a 115 directory root and reconciles permanent video assets.
func (s *TreeScanner) ScanCID(ctx context.Context, rootCID string, mode string) (*ScanStats, error) {
	s.logger.Info("starting 115 directory scan", "root_cid", rootCID, "mode", mode)

	// 1. Get active binding
	binding, err := s.assetRepo.GetActiveBinding(ctx, "115")
	if err != nil || binding == nil {
		return nil, fmt.Errorf("active 115 binding not found")
	}

	// 2. Create job and scan_run
	scanJobID := "job_scan_" + uuid.New().String()[:8]
	_, _, err = s.jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:         scanJobID,
		Kind:       "scan",
		DedupeKey:  fmt.Sprintf("scan:%s:%s", binding.ID, rootCID),
		BindingID:  &binding.ID,
		ParamsJSON: fmt.Sprintf(`{"root_cid":"%s","mode":"%s"}`, rootCID, mode),
	})
	if err != nil {
		return nil, fmt.Errorf("create scan job: %w", err)
	}

	scanRunID := "scan_" + uuid.New().String()[:8]
	now := models.UTCNow()
	scanRun := &models.ScanRun{
		ID:        scanRunID,
		JobID:     scanJobID,
		BindingID: binding.ID,
		RootID:    rootCID,
		Mode:      mode,
		State:     "running",
		StartedAt: now,
	}
	if err := s.assetRepo.CreateScanRun(ctx, scanRun); err != nil {
		return nil, fmt.Errorf("create scan run: %w", err)
	}

	// 3. List recursive videos from 115
	files, err := s.listAllRecursive(ctx, rootCID)
	if err != nil {
		errMsg := err.Error()
		_ = s.assetRepo.CompleteScanRun(ctx, scanRunID, "failed", &errMsg)
		_ = s.jobRepo.FinishJob(ctx, scanJobID, "failed", "{}", &errMsg)
		return nil, fmt.Errorf("list directory recursive: %w", err)
	}

	stats := &ScanStats{TotalFilesSeen: len(files)}
	affectedMovies := make(map[string]bool)

	// 4. Process files
	for _, f := range files {
		// Filter format: only mp4, mkv, ts
		ext := strings.ToLower(filepath.Ext(f.FileName))
		if ext != ".mp4" && ext != ".mkv" && ext != ".ts" {
			continue
		}

		// Filter size: >= 100MiB (104857600 bytes)
		if f.SizeBytes < 104857600 {
			continue
		}

		// Filter samples and trailers
		lowerName := strings.ToLower(f.FileName)
		if strings.Contains(lowerName, "sample") || strings.Contains(lowerName, "trailer") || strings.Contains(lowerName, "preview") {
			continue
		}

		stats.ValidVideos++

		// Extract code from filename
		code, reason := identity.NormalizeCode(f.FileName)
		if code == "" || reason == "unsupported_number_length" || reason == "ambiguous" {
			// Try parent directory if available
			if f.ParentID != "" {
				pCode, pReason := identity.NormalizeCode(f.ParentID)
				if pCode != "" && pReason == "" {
					code = pCode
				}
			}
		}

		if code == "" {
			_ = s.assetRepo.RecordScanSeen(ctx, scanRunID, f.FileID, nil)
			continue
		}

		// Check if movie exists in DB
		movie, err := s.movieRepo.GetMovie(ctx, code)
		if err != nil || movie == nil {
			_ = s.assetRepo.RecordScanSeen(ctx, scanRunID, f.FileID, nil)
			continue
		}

		stats.MatchedMovies++
		resourceKey := fmt.Sprintf("115:%s:%s", binding.ID, f.FileID)
		_ = s.assetRepo.RecordScanSeen(ctx, scanRunID, f.FileID, &resourceKey)

		// Merge into DB
		err = s.database.ExecWrite(ctx, func(tx *sql.Tx) error {
			// 1. Insert/update magnet as existing resource
			quality := identity.ExtractQualityFlags(f.FileName)
			qualityLabel := "1080P"
			if quality.Is4K == 1 {
				qualityLabel = "4K"
			}

			_, err := tx.ExecContext(ctx, `
				INSERT INTO offline_magnets (
					info_hash, movie_code, resource_kind, magnet_url, title, size_bytes,
					quality_label, website, has_chinese_sub, is_cracked, is_4k, is_censored,
					is_preferred, enabled, metadata_sources, manual_fields, legacy_runtime,
					created_at, updated_at
				) VALUES (?, ?, 'existing', ?, ?, ?, ?, '115_existing', ?, ?, ?, ?, 0, 1, '{}', '[]', '{}', ?, ?)
				ON CONFLICT(info_hash) DO UPDATE SET
					title = excluded.title,
					size_bytes = excluded.size_bytes,
					quality_label = excluded.quality_label,
					updated_at = excluded.updated_at;
			`, resourceKey, code, resourceKey, f.FileName, f.SizeBytes, qualityLabel,
				quality.HasChineseSub, quality.IsCracked, quality.Is4K, quality.IsCensored, now, now)
			if err != nil {
				return err
			}

			// 2. Insert/update permanent cloud_asset
			assetID := "ast_" + uuid.New().String()[:8]
			_, err = tx.ExecContext(ctx, `
				INSERT INTO cloud_assets (
					id, binding_id, resource_key, source_type, state, generation,
					file_id, pick_code, file_name, size_bytes, container, parent_id,
					manifest_json, last_seen_at, created_at, updated_at
				) VALUES (?, ?, ?, 'permanent', 'ready', 1, ?, ?, ?, ?, ?, ?, '[]', ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET
					pick_code = excluded.pick_code,
					file_name = excluded.file_name,
					size_bytes = excluded.size_bytes,
					last_seen_at = excluded.last_seen_at,
					updated_at = excluded.updated_at;
			`, assetID, binding.ID, resourceKey, f.FileID, f.PickCode, f.FileName, f.SizeBytes,
				strings.TrimPrefix(ext, "."), f.ParentID, now, now, now)
			if err != nil {
				return err
			}

			return nil
		})

		if err == nil {
			stats.AssetsCreated++
			affectedMovies[code] = true
		}
	}

	// 5. Recompute preferred magnet for all affected movies
	for code := range affectedMovies {
		_ = s.database.ExecWrite(ctx, func(tx *sql.Tx) error {
			return db.RecomputePreferredMagnetTx(ctx, tx, code)
		})
	}

	// 6. Finish scan_run and job
	_ = s.assetRepo.CompleteScanRun(ctx, scanRunID, "completed", nil)
	_ = s.jobRepo.FinishJob(ctx, scanJobID, "succeeded", fmt.Sprintf(`{"files":%d,"matched":%d}`, stats.TotalFilesSeen, stats.MatchedMovies), nil)

	s.logger.Info("scan completed successfully", "stats", stats)
	return stats, nil
}

func (s *TreeScanner) listAllRecursive(ctx context.Context, cid string) ([]client115.FileItem, error) {
	var allFiles []client115.FileItem
	offset := 0
	limit := 1150
	for {
		items, total, err := s.c115Client.ListFilesRecursive(ctx, cid, limit, offset)
		if err != nil {
			return nil, err
		}
		allFiles = append(allFiles, items...)
		offset += len(items)
		if len(items) == 0 || offset >= total {
			break
		}
	}
	return allFiles, nil
}
