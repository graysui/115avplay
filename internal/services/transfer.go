package services

import (
	"context"
	"database/sql"
	"errors"
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
	tm.logger.Info("开始转存临时资源", "任务", job.ID, "info_hash", magnet.InfoHash, "影片", magnet.MovieCode)

	// 1. 在 tempRootCID 下创建独立临时目录
	folderName := fmt.Sprintf("mv_tmp_%s", job.ID)
	tempFolderID, err := tm.c115Client.CreateFolder(ctx, tm.tempRootCID, folderName)
	if err != nil {
		errMsg := fmt.Sprintf("create temp folder: %v", err)
		tm.logger.Warn("转存失败：创建临时目录出错", "任务", job.ID, "错误", err.Error())
		_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
		return
	}
	tm.logger.Info("已创建临时目录", "任务", job.ID, "目录CID", tempFolderID)

	// 2. 提交离线任务（OpenAPI 仅支持 add_task_urls，磁力/ed2k 统一按 URL 提交）
	resourceURL := magnet.MagnetURL
	if resourceURL == "" && magnet.ResourceKind == "btih" {
		resourceURL = "magnet:?xt=urn:btih:" + magnet.InfoHash
	}
	if resourceURL == "" {
		errMsg := fmt.Sprintf("unsupported resource kind for transfer: %s", magnet.ResourceKind)
		tm.logger.Warn("转存失败：不支持的资源类型", "任务", job.ID, "类型", magnet.ResourceKind)
		_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
		return
	}

	remoteHash, err := tm.c115Client.AddURLTask(ctx, resourceURL, tempFolderID)
	if err != nil {
		// 115 对磁力任务做全局去重：若该 info_hash 已有离线任务会返回
		// code=10008「任务已存在」。此时复用已有任务并清理刚建的空目录，
		// 而不是转入无法推进的对账状态。
		if isTaskExistsError(err) {
			if existing, lerr := tm.c115Client.FindTaskByInfoHash(ctx, magnet.InfoHash, 8); lerr == nil && existing != nil {
				tm.logger.Info("115 提示任务已存在，复用已有离线任务",
					"任务", job.ID, "影片", magnet.MovieCode,
					"状态", existing.DisplayStatus, "进度", existing.PercentDone)
				tm.removeFolderQuietly(ctx, tempFolderID)
				tm.pollAndRegister(ctx, job, binding, magnet, existing.WpPathID, existing.InfoHash)
				return
			}
			tm.logger.Warn("115 提示任务已存在但未找到对应任务，改为轮询目录",
				"任务", job.ID, "影片", magnet.MovieCode)
			tm.pollAndRegister(ctx, job, binding, magnet, tempFolderID, magnet.InfoHash)
			return
		}
		tm.logger.Warn("提交 115 离线任务失败，转入对账", "任务", job.ID, "错误", err.Error())
		_ = tm.jobRepo.SetJobReconcile(ctx, job.ID, remoteHash, err.Error())
		return
	}
	tm.logger.Info("已提交 115 离线任务", "任务", job.ID, "info_hash", remoteHash)

	tm.pollAndRegister(ctx, job, binding, magnet, tempFolderID, remoteHash)
}

// isTaskExistsError reports whether 115 rejected the add because the magnet task
// is already registered (code 10008 / 任务已存在).
func isTaskExistsError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *client115.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode == 10008 {
		return true
	}
	return strings.Contains(err.Error(), "10008") || strings.Contains(err.Error(), "任务已存在")
}

// removeFolderQuietly deletes an (empty) temp folder, ignoring errors.
func (tm *TransferManager) removeFolderQuietly(ctx context.Context, folderID string) {
	if folderID == "" {
		return
	}
	if err := tm.c115Client.DeleteFile(ctx, []string{folderID}); err != nil {
		tm.logger.Warn("清理空临时目录失败（忽略）", "目录CID", folderID, "错误", err.Error())
		return
	}
	tm.logger.Info("已清理未使用的临时目录", "目录CID", folderID)
}

// readyFile describes a video file that is ready to be registered as a temporary asset.
type readyFile struct {
	FileID    string
	PickCode  string
	FileName  string
	ParentID  string
	Container string
	SizeBytes int64
}

var videoExtensions = map[string]bool{
	".mp4": true, ".mkv": true, ".ts": true, ".avi": true,
	".mov": true, ".wmv": true, ".flv": true, ".m2ts": true, ".rmvb": true,
}

// pollAndRegister waits for the offline task to finish, locates the video file
// and registers a temporary ready asset.
func (tm *TransferManager) pollAndRegister(ctx context.Context, job *models.Job, binding *models.CloudBinding, magnet *models.Magnet, folderID, remoteHash string) {
	pollCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	noFileTicks := 0
	for {
		select {
		case <-pollCtx.Done():
			errMsg := "transfer timed out"
			tm.logger.Warn("转存超时", "任务", job.ID, "info_hash", magnet.InfoHash)
			_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
			return
		case <-ticker.C:
			matched := tm.lookupTask(pollCtx, magnet.InfoHash, remoteHash)
			if matched != nil && matched.Status == -1 {
				errMsg := fmt.Sprintf("115 offline task failed: %s", matched.StatusText)
				tm.logger.Warn("转存失败：115 离线任务失败", "任务", job.ID, "影片", magnet.MovieCode, "状态", matched.StatusText)
				_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
				return
			}

			isCompleted := matched == nil || matched.Status == 2 || matched.PercentDone >= 100.0
			if !isCompleted {
				continue
			}

			rf := tm.locateReadyFile(pollCtx, folderID, matched)
			if rf == nil {
				noFileTicks++
				// 任务显示已完成但在目标目录找不到视频：可能是旧任务的内容已被删除。
				if noFileTicks >= 12 {
					errMsg := "offline task finished but no video file found in destination folder"
					tm.logger.Warn("转存失败：未找到视频文件", "任务", job.ID, "影片", magnet.MovieCode, "目录CID", folderID)
					_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
					return
				}
				continue
			}
			tm.registerTemporaryAsset(pollCtx, job, binding, magnet, rf, folderID)
			return
		}
	}
}

// lookupTask finds the offline task by remote hash, falling back to a full scan.
func (tm *TransferManager) lookupTask(ctx context.Context, infoHash, remoteHash string) *client115.OfflineTask {
	if tasks, _, err := tm.c115Client.GetTaskList(ctx, 1); err == nil {
		for i := range tasks {
			if (remoteHash != "" && strings.EqualFold(tasks[i].InfoHash, remoteHash)) ||
				strings.EqualFold(tasks[i].InfoHash, infoHash) {
				return &tasks[i]
			}
		}
	}
	if t, err := tm.c115Client.FindTaskByInfoHash(ctx, infoHash, 8); err == nil {
		return t
	}
	return nil
}

// locateReadyFile finds the largest video file in the destination folder(s).
func (tm *TransferManager) locateReadyFile(ctx context.Context, folderID string, task *client115.OfflineTask) *readyFile {
	folders := make([]string, 0, 3)
	if folderID != "" {
		folders = append(folders, folderID)
	}
	if task != nil {
		// 已完成任务的内容可能不在 wp_path_id（历史任务的目的目录可能已变更），
		// 而位于任务自身的 file_id 目录中，两者都尝试。
		for _, cid := range []string{task.WpPathID, task.FileID} {
			if cid == "" || cid == folderID {
				continue
			}
			dup := false
			for _, f := range folders {
				if f == cid {
					dup = true
					break
				}
			}
			if !dup {
				folders = append(folders, cid)
			}
		}
	}

	for _, fid := range folders {
		files, _, err := tm.c115Client.ListFilesRecursive(ctx, fid, 100, 0)
		if err != nil || len(files) == 0 {
			continue
		}
		var best *client115.FileItem
		for i := range files {
			f := files[i]
			if videoExtensions[strings.ToLower(filepath.Ext(f.FileName))] {
				if best == nil || f.SizeBytes > best.SizeBytes {
					best = &f
				}
			}
		}
		if best != nil {
			return &readyFile{
				FileID:    best.FileID,
				PickCode:  best.PickCode,
				FileName:  best.FileName,
				ParentID:  best.ParentID,
				Container: strings.TrimPrefix(filepath.Ext(best.FileName), "."),
				SizeBytes: best.SizeBytes,
			}
		}
	}

	// 注意：任务的 pick_code 是任务自身的取码（downurl 返回空），不能当作文件取码，
	// 因此只能依靠目录列举拿到真实文件。
	return nil
}

// registerTemporaryAsset inserts a ready temporary cloud_asset for the file.
func (tm *TransferManager) registerTemporaryAsset(ctx context.Context, job *models.Job, binding *models.CloudBinding, magnet *models.Magnet, rf *readyFile, ownedRootID string) {
	now := models.UTCNow()
	nowTime := time.Now().UTC()
	expiresAt := nowTime.Add(time.Duration(tm.ttlDays) * 24 * time.Hour).Format(time.RFC3339)
	assetID := "ast_tmp_" + uuid.New().String()[:8]
	if rf.ParentID == "" {
		rf.ParentID = ownedRootID
	}

	err := tm.database.ExecWrite(ctx, func(tx *sql.Tx) error {
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
		`, assetID, binding.ID, magnet.InfoHash, job.ID, rf.FileID, rf.PickCode,
			rf.FileName, rf.SizeBytes, rf.Container,
			rf.ParentID, ownedRootID, ownedRootID, now, expiresAt, now, now, now)
		if err != nil {
			return err
		}
		return db.RecomputePreferredMagnetTx(ctx, tx, magnet.MovieCode)
	})

	if err != nil {
		errMsg := fmt.Sprintf("register asset: %v", err)
		tm.logger.Warn("转存失败：注册临时资产出错", "任务", job.ID, "错误", err.Error())
		_ = tm.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
		return
	}

	_ = tm.jobRepo.FinishJob(ctx, job.ID, "succeeded", fmt.Sprintf(`{"asset_id":"%s","file_id":"%s"}`, assetID, rf.FileID), nil)
	tm.logger.Info("转存完成，临时资产已就绪", "任务", job.ID, "影片", magnet.MovieCode, "资产", assetID, "文件", rf.FileName, "失效时间", expiresAt)
}
