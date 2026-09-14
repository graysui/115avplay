package services

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"mediavault/internal/client115"
	"mediavault/internal/db"
	"mediavault/internal/models"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

type TransferManager struct {
	database    *db.DB
	assetRepo   *db.AssetRepo
	magnetRepo  *db.MagnetRepo
	jobRepo     *db.JobRepo
	c115Client  *client115.Client
	tempRootCID string
	ttlDays     int
	logger      *slog.Logger
}

func NewTransferManager(
	database *db.DB,
	assetRepo *db.AssetRepo,
	magnetRepo *db.MagnetRepo,
	jobRepo *db.JobRepo,
	c115Client *client115.Client,
	tempRootCID string,
	ttlDays int,
	logger *slog.Logger,
) *TransferManager {
	if ttlDays <= 0 {
		ttlDays = 7
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &TransferManager{
		database:    database,
		assetRepo:   assetRepo,
		magnetRepo:  magnetRepo,
		jobRepo:     jobRepo,
		c115Client:  c115Client,
		tempRootCID: tempRootCID,
		ttlDays:     ttlDays,
		logger:      logger,
	}
}

// StartTransfer initiates or retrieves an active transfer job for a magnet.
func (tm *TransferManager) StartTransfer(ctx context.Context, binding *models.CloudBinding, magnet *models.Magnet) (*models.Job, error) {
	if tm.tempRootCID == "" {
		return nil, fmt.Errorf("temp_transfer_cid not configured, cannot accept new transfer")
	}

	dedupeKey := fmt.Sprintf("%s:%s", binding.ID, magnet.InfoHash)

	job, created, err := tm.jobRepo.CreateOrGetJob(ctx, &models.Job{
		Kind:        "transfer",
		DedupeKey:   dedupeKey,
		ResourceKey: &magnet.InfoHash,
		BindingID:   &binding.ID,
		ParamsJSON:  fmt.Sprintf(`{"info_hash":"%s","movie_code":"%s"}`, magnet.InfoHash, magnet.MovieCode),
	})
	if err != nil {
		return nil, fmt.Errorf("create transfer job: %w", err)
	}

	if !created && (job.State == "running" || job.State == "queued" || job.State == "reconcile") {
		// Existing active transfer job
		return job, nil
	}

	workerID := "transfer-worker-" + uuid.New().String()[:8]
	claimed, err := tm.jobRepo.ClaimJobByID(ctx, job.ID, workerID, 7200*time.Second)
	if err != nil || claimed == nil {
		return job, nil
	}

	go tm.executeTransfer(context.Background(), claimed, binding, magnet)
	return claimed, nil
}

func (tm *TransferManager) executeTransfer(ctx context.Context, job *models.Job, binding *models.CloudBinding, magnet *models.Magnet) {
	tm.logger.Info("starting transfer execution", "job_id", job.ID, "info_hash", magnet.InfoHash)

	// 1. Create exclusive dedicated subfolder in tempRootCID
	folderName := fmt.Sprintf("mv_tmp_%s", job.ID)
	tempFolderID, err := tm.c115Client.CreateFolder(ctx, tm.tempRootCID, folderName)
	if err != nil {
		errMsg := fmt.Sprintf("create temp folder: %v", err)
		_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
		return
	}

	// 2. Submit offline task to 115
	var remoteHash string
	if magnet.ResourceKind == "btih" {
		remoteHash, err = tm.c115Client.AddBTTask(ctx, magnet.InfoHash, tempFolderID)
	} else if magnet.ResourceKind == "ed2k" {
		remoteHash, err = tm.c115Client.AddURLTask(ctx, magnet.MagnetURL, tempFolderID)
	} else {
		errMsg := fmt.Sprintf("unsupported resource kind for transfer: %s", magnet.ResourceKind)
		_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
		return
	}

	if err != nil {
		// If error indicates unknown status, move to reconcile
		tm.logger.Warn("offline submission failed or returned unknown, setting reconcile", "job_id", job.ID, "error", err)
		_ = tm.jobRepo.SetJobReconcile(ctx, job.ID, remoteHash, err.Error())
		return
	}

	// 3. Poll offline task status until ready (timeout 2 hours)
	pollCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pollCtx.Done():
			errMsg := "transfer timed out"
			_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
			return
		case <-ticker.C:
			tasks, _, err := tm.c115Client.GetTaskList(pollCtx, 1)
			if err != nil {
				continue
			}

			var matchedTask *client115.OfflineTask
			for _, t := range tasks {
				if t.InfoHash == remoteHash || t.InfoHash == magnet.InfoHash {
					matchedTask = &t
					break
				}
			}

			// If task is completed (status == 2, percentDone == 100) or not found in task list
			// (completed tasks sometimes disappear from active task list into the destination folder)
			isCompleted := false
			if matchedTask != nil && (matchedTask.Status == 2 || matchedTask.PercentDone >= 100.0) {
				isCompleted = true
			} else if matchedTask == nil {
				// Check destination folder directly
				isCompleted = true
			}

			if isCompleted {
				// Locate video file in tempFolderID
				files, _, err := tm.c115Client.ListFilesRecursive(pollCtx, tempFolderID, 100, 0)
				if err != nil || len(files) == 0 {
					continue
				}

				// Find best video file (largest .mp4/.mkv/.ts)
				var bestFile *client115.FileItem
				for _, f := range files {
					ext := strings.ToLower(filepath.Ext(f.FileName))
					if ext == ".mp4" || ext == ".mkv" || ext == ".ts" {
						if bestFile == nil || f.SizeBytes > bestFile.SizeBytes {
							bestFile = &f
						}
					}
				}

				if bestFile == nil {
					continue
				}

				// 4. Register temporary ready cloud_asset
				now := models.UTCNow()
				nowTime := time.Now().UTC()
				expiresAt := nowTime.Add(time.Duration(tm.ttlDays) * 24 * time.Hour).Format(time.RFC3339)
				assetID := "ast_tmp_" + uuid.New().String()[:8]

				err = tm.database.ExecWrite(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `
						INSERT INTO cloud_assets (
							id, binding_id, resource_key, owning_job_id, source_type, state, generation,
							file_id, pick_code, file_name, size_bytes, container, parent_id,
							owned_root_id, root_snapshot, manifest_json, ready_at, expires_at,
							last_seen_at, created_at, updated_at
						) VALUES (?, ?, ?, ?, 'temporary', 'ready', 1, ?, ?, ?, ?, ?, ?, ?, ?, '[]', ?, ?, ?, ?, ?)
						ON CONFLICT(id) DO UPDATE SET
							state = 'ready',
							expires_at = excluded.expires_at,
							updated_at = excluded.updated_at;
					`, assetID, binding.ID, magnet.InfoHash, job.ID, bestFile.FileID, bestFile.PickCode,
						bestFile.FileName, bestFile.SizeBytes, strings.TrimPrefix(filepath.Ext(bestFile.FileName), "."),
						bestFile.ParentID, tempFolderID, tempFolderID, now, expiresAt, now, now, now)
					if err != nil {
						return err
					}

					return db.RecomputePreferredMagnetTx(ctx, tx, magnet.MovieCode)
				})

				if err != nil {
					errMsg := fmt.Sprintf("register asset: %v", err)
					_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
					return
				}

				_ = tm.jobRepo.FinishJob(ctx, job.ID, "succeeded", fmt.Sprintf(`{"asset_id":"%s","file_id":"%s"}`, assetID, bestFile.FileID), nil)
				tm.logger.Info("transfer completed successfully", "job_id", job.ID, "asset_id", assetID)
				return
			}
		}
	}
}
