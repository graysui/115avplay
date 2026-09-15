package services

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"mediavault/internal/db"
)

// RetentionManager enforces storage budgets, cache size limits, and data retention policies.
type RetentionManager struct {
	database     *db.DB
	settingsRepo *db.SettingsRepo
	alertEngine  *AlertEngine
	dataDir      string
	logger       *slog.Logger
}

func NewRetentionManager(
	database *db.DB,
	settingsRepo *db.SettingsRepo,
	alertEngine *AlertEngine,
	dataDir string,
	logger *slog.Logger,
) *RetentionManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &RetentionManager{
		database:     database,
		settingsRepo: settingsRepo,
		alertEngine:  alertEngine,
		dataDir:      dataDir,
		logger:       logger,
	}
}

// CheckDiskSpace checks if available disk space meets the minimum requirement (disk_min_free_bytes).
func (rm *RetentionManager) CheckDiskSpace(ctx context.Context) (bool, uint64, uint64, error) {
	minFreeBytes := uint64(1073741824) // 1 GiB default
	if s, err := rm.settingsRepo.GetSetting(ctx, "disk_min_free_bytes"); err == nil && s != nil && s.Value != nil {
		if val, err := strconv.ParseUint(*s.Value, 10, 64); err == nil && val > 0 {
			minFreeBytes = val
		}
	}

	freeBytes, err := GetFreeDiskSpace(rm.dataDir)
	if err != nil {
		rm.logger.Warn("Failed to check free disk space", "error", err.Error())
		return true, 0, minFreeBytes, nil // Fallback to allowing on query error
	}

	if freeBytes < minFreeBytes {
		rm.logger.Warn("Available disk space is below minimum threshold", "free_bytes", freeBytes, "min_free_bytes", minFreeBytes)
		if rm.alertEngine != nil {
			rm.alertEngine.AlertDiskLow(ctx, freeBytes, minFreeBytes)
		}
		return false, freeBytes, minFreeBytes, nil
	}

	// Resolve alert if space recovered
	if rm.alertEngine != nil {
		_ = rm.alertEngine.ResolveAlert(ctx, "disk_space_low")
	}

	return true, freeBytes, minFreeBytes, nil
}

type fileInfo struct {
	path    string
	size    int64
	modTime time.Time
}

// PruneImageCache trims the image cache down to 80% of image_cache_bytes using LRU.
func (rm *RetentionManager) PruneImageCache(ctx context.Context) (int64, error) {
	cacheDir := filepath.Join(rm.dataDir, "cache", "images")
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		return 0, nil
	}

	maxBytes := int64(2147483648) // 2 GiB default
	if s, err := rm.settingsRepo.GetSetting(ctx, "image_cache_bytes"); err == nil && s != nil && s.Value != nil {
		if val, err := strconv.ParseInt(*s.Value, 10, 64); err == nil && val > 0 {
			maxBytes = val
		}
	}

	var files []fileInfo
	var totalSize int64

	err := filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		files = append(files, fileInfo{
			path:    path,
			size:    info.Size(),
			modTime: info.ModTime(),
		})
		totalSize += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}

	if totalSize <= maxBytes {
		return 0, nil // Under budget
	}

	// Target 80% of maxBytes per spec
	targetSize := (maxBytes * 8) / 10

	// Sort files by modTime ASC (oldest accessed/modified first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.Before(files[j].modTime)
	})

	var freedBytes int64
	for _, f := range files {
		if totalSize-freedBytes <= targetSize {
			break
		}
		if err := os.Remove(f.path); err == nil {
			freedBytes += f.size
		}
	}

	rm.logger.Info("Pruned image cache", "freed_bytes", freedBytes, "new_total", totalSize-freedBytes, "target", targetSize)
	return freedBytes, nil
}

// PruneOldLogs removes log files older than log_retention_days or exceeding log_max_bytes.
func (rm *RetentionManager) PruneOldLogs(ctx context.Context) (int, error) {
	logsDir := filepath.Join(rm.dataDir, "logs")
	if _, err := os.Stat(logsDir); os.IsNotExist(err) {
		return 0, nil
	}

	retentionDays := 7
	if s, err := rm.settingsRepo.GetSetting(ctx, "log_retention_days"); err == nil && s != nil && s.Value != nil {
		if val, err := strconv.Atoi(*s.Value); err == nil && val > 0 {
			retentionDays = val
		}
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	var removedCount int

	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return 0, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			filePath := filepath.Join(logsDir, entry.Name())
			if err := os.Remove(filePath); err == nil {
				removedCount++
			}
		}
	}

	return removedCount, nil
}

// PruneCompletedJobs removes old completed/failed jobs beyond retention, protecting referenced jobs.
func (rm *RetentionManager) PruneCompletedJobs(ctx context.Context) (int64, error) {
	retentionDays := 30
	if s, err := rm.settingsRepo.GetSetting(ctx, "job_retention_days"); err == nil && s != nil && s.Value != nil {
		if val, err := strconv.Atoi(*s.Value); err == nil && val > 0 {
			retentionDays = val
		}
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339)

	var deletedCount int64
	err := rm.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		// Only delete terminal jobs that are:
		// 1. state IN ('completed', 'failed', 'cancelled')
		// 2. completed_at < cutoff
		// 3. NOT referenced by cloud_assets(owning_job_id)
		// 4. NOT referenced by scan_runs(job_id)
		// 5. NOT referenced by ingest_assets(run_id)
		query := `
			DELETE FROM jobs
			WHERE state IN ('succeeded', 'failed', 'cancelled')
			  AND completed_at IS NOT NULL
			  AND completed_at < ?
			  AND id NOT IN (SELECT owning_job_id FROM cloud_assets WHERE owning_job_id IS NOT NULL)
			  AND id NOT IN (SELECT job_id FROM scan_runs WHERE job_id IS NOT NULL)
			  AND id NOT IN (SELECT run_id FROM ingest_assets WHERE run_id IS NOT NULL);
		`
		res, err := tx.ExecContext(ctx, query, cutoff)
		if err != nil {
			return err
		}
		deletedCount, _ = res.RowsAffected()
		return nil
	})

	return deletedCount, err
}
