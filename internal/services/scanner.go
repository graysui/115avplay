package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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
	s.logger.Info("开始扫描永久媒体库", "根目录CID", rootCID, "模式", mode)

	binding, err := s.assetRepo.GetActiveBinding(ctx, "115")
	if err != nil || binding == nil {
		s.logger.Warn("扫描终止：未绑定 115 账号", "根目录CID", rootCID)
		return nil, fmt.Errorf("active 115 binding not found")
	}

	scanJobID := "job_scan_" + uuid.New().String()[:8]
	if _, _, err := s.jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:         scanJobID,
		Kind:       "scan",
		DedupeKey:  fmt.Sprintf("scan:%s:%s", binding.ID, rootCID),
		BindingID:  &binding.ID,
		ParamsJSON: fmt.Sprintf(`{"root_cid":"%s","mode":"%s"}`, rootCID, mode),
	}); err != nil {
		return nil, fmt.Errorf("create scan job: %w", err)
	}

	scanRunID := "scan_" + uuid.New().String()[:8]
	now := models.UTCNow()
	if err := s.assetRepo.CreateScanRun(ctx, &models.ScanRun{
		ID:        scanRunID,
		JobID:     scanJobID,
		BindingID: binding.ID,
		RootID:    rootCID,
		Mode:      mode,
		State:     "running",
		StartedAt: now,
	}); err != nil {
		return nil, fmt.Errorf("create scan run: %w", err)
	}

	stats := &ScanStats{}
	affectedMovies := make(map[string]bool)

	fail := func(err error) (*ScanStats, error) {
		errMsg := err.Error()
		_ = s.assetRepo.CompleteScanRun(ctx, scanRunID, "failed", &errMsg)
		_ = s.jobRepo.FinishJob(ctx, scanJobID, "failed", "{}", &errMsg)
		s.logger.Warn("扫描失败", "根目录CID", rootCID, "错误", errMsg)
		return nil, err
	}

	// Prefer the cookie bulk "download nodes" API (fast, whole subtree). Fall back
	// to per-directory OpenAPI traversal when no cookie is configured.
	if strings.TrimSpace(s.c115Client.GetCookie()) != "" {
		if err := s.scanBulk(ctx, rootCID, binding, scanRunID, stats, affectedMovies); err != nil {
			return fail(err)
		}
	} else {
		if err := s.scanWalk(ctx, rootCID, binding, scanRunID, stats, affectedMovies); err != nil {
			return fail(err)
		}
	}

	// Recompute preferred magnet for all affected movies
	for code := range affectedMovies {
		_ = s.database.ExecWrite(ctx, func(tx *sql.Tx) error {
			return db.RecomputePreferredMagnetTx(ctx, tx, code)
		})
	}

	_ = s.assetRepo.CompleteScanRun(ctx, scanRunID, "completed", nil)
	_ = s.jobRepo.FinishJob(ctx, scanJobID, "succeeded", fmt.Sprintf(`{"files":%d,"matched":%d}`, stats.TotalFilesSeen, stats.MatchedMovies), nil)
	s.logger.Info("永久媒体库扫描完成",
		"根目录CID", rootCID,
		"扫描文件", stats.TotalFilesSeen,
		"有效视频", stats.ValidVideos,
		"匹配影片", stats.MatchedMovies,
		"新建永久资产", stats.AssetsCreated)
	return stats, nil
}

// scanWalk traverses the tree directory-by-directory (OpenAPI / web cookie).
func (s *TreeScanner) scanWalk(ctx context.Context, rootCID string, binding *models.CloudBinding, scanRunID string, stats *ScanStats, affected map[string]bool) error {
	var files []client115.FileItem
	err := s.c115Client.WalkTree(ctx, rootCID,
		func(dirsSeen, filesSeen int) {
			s.logger.Info("永久媒体库扫描：目录遍历进度", "已遍历目录", dirsSeen, "已发现文件", filesSeen)
		},
		func(cid string, derr error) {
			s.logger.Warn("永久媒体库扫描：跳过无法访问的目录", "目录CID", cid, "错误", derr.Error())
		},
		func(f client115.FileItem) error {
			files = append(files, f)
			return nil
		},
	)
	if err != nil {
		return fmt.Errorf("walk directory tree: %w", err)
	}
	stats.TotalFilesSeen = len(files)
	s.logger.Info("永久媒体库扫描：目录遍历完成", "根目录CID", rootCID, "文件数", len(files))

	for _, f := range files {
		ext := strings.ToLower(filepath.Ext(f.FileName))
		if ext != ".mp4" && ext != ".mkv" && ext != ".ts" {
			continue
		}
		code, reason := identity.NormalizeCode(f.FileName)
		if code == "" || reason == "unsupported_number_length" || reason == "ambiguous" {
			continue
		}
		fileKey := f.PickCode
		if fileKey == "" {
			fileKey = f.FileID
		}
		s.processCandidate(ctx, binding, scanRunID, stats, affected, code, fileKey, f.PickCode, f.FileName, f.SizeBytes, f.ParentID)
	}
	return nil
}

// scanBulk uses the "download nodes" APIs to fetch the whole subtree in a few
// paginated requests and registers the largest file of each folder.
func (s *TreeScanner) scanBulk(ctx context.Context, rootCID string, binding *models.CloudBinding, scanRunID string, stats *ScanStats, affected map[string]bool) error {
	rootPC, err := s.c115Client.GetNodePickCode(ctx, rootCID)
	if err != nil {
		return fmt.Errorf("获取根目录 pickcode: %w", err)
	}
	if rootPC == "" {
		return fmt.Errorf("无法获取根目录 pickcode（Cookie 是否有效？）")
	}
	s.logger.Info("永久媒体库扫描：使用穿透接口拉取目录树", "根目录CID", rootCID, "pickcode", rootPC)

	folders, err := s.c115Client.DownloadFolders(ctx, rootPC)
	if err != nil {
		return fmt.Errorf("拉取目录列表: %w", err)
	}
	s.logger.Info("永久媒体库扫描：目录获取完成", "目录数", len(folders))

	files, err := s.c115Client.DownloadFiles(ctx, rootPC)
	if err != nil {
		return fmt.Errorf("拉取文件列表: %w", err)
	}
	stats.TotalFilesSeen = len(files)
	s.logger.Info("永久媒体库扫描：文件获取完成", "文件数", len(files))

	folderName := make(map[string]string, len(folders))
	folderParent := make(map[string]string, len(folders))
	for _, f := range folders {
		folderName[f.FID] = f.Name
		folderParent[f.FID] = f.PID
	}

	// Keep the largest file per folder (the main feature).
	type bestFile struct {
		pickCode string
		size     int64
	}
	best := make(map[string]*bestFile)
	for _, f := range files {
		if f.PID == "" {
			continue
		}
		if b := best[f.PID]; b == nil || f.SizeBytes > b.size {
			best[f.PID] = &bestFile{pickCode: f.PickCode, size: f.SizeBytes}
		}
	}
	s.logger.Info("永久媒体库扫描：合并版本目录完成", "候选目录数", len(best))

	for pid, b := range best {
		code := s.codeFromAncestors(pid, folderName, folderParent, 4)
		if code == "" {
			continue
		}
		s.processCandidate(ctx, binding, scanRunID, stats, affected, code, b.pickCode, b.pickCode, folderName[pid], b.size, pid)
	}
	return nil
}

// codeFromAncestors extracts a movie code from the folder name, walking up the
// parent chain up to maxDepth levels.
func (s *TreeScanner) codeFromAncestors(fid string, names, parents map[string]string, maxDepth int) string {
	cur := fid
	for depth := 0; depth < maxDepth && cur != ""; depth++ {
		name, ok := names[cur]
		if ok {
			if code, reason := identity.NormalizeCode(name); code != "" && reason != "unsupported_number_length" && reason != "ambiguous" {
				return code
			}
		}
		cur = parents[cur]
	}
	return ""
}

// processCandidate filters one video candidate and upserts its permanent asset.
func (s *TreeScanner) processCandidate(ctx context.Context, binding *models.CloudBinding, scanRunID string, stats *ScanStats, affected map[string]bool, code, fileID, pickCode, fileName string, sizeBytes int64, parentID string) {
	if sizeBytes < 104857600 {
		return
	}
	lowerName := strings.ToLower(fileName)
	if strings.Contains(lowerName, "sample") || strings.Contains(lowerName, "trailer") || strings.Contains(lowerName, "preview") {
		return
	}
	stats.ValidVideos++

	movie, err := s.movieRepo.GetMovie(ctx, code)
	if err != nil || movie == nil {
		_ = s.assetRepo.RecordScanSeen(ctx, scanRunID, fileID, nil)
		return
	}
	stats.MatchedMovies++
	resourceKey := fmt.Sprintf("115:%s:%s", binding.ID, fileID)
	_ = s.assetRepo.RecordScanSeen(ctx, scanRunID, fileID, &resourceKey)

	now := models.UTCNow()
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(fileName)), ".")
	if ext == "" {
		ext = "mp4"
	}
	quality := identity.ExtractQualityFlags(fileName)
	qualityLabel := "1080P"
	if quality.Is4K == 1 {
		qualityLabel = "4K"
	}

	// Deterministic asset id so re-scans update instead of duplicating.
	sum := sha256.Sum256([]byte(resourceKey))
	assetID := "ast_perm_" + hex.EncodeToString(sum[:8])

	err = s.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
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
		`, resourceKey, code, resourceKey, fileName, sizeBytes, qualityLabel,
			quality.HasChineseSub, quality.IsCracked, quality.Is4K, quality.IsCensored, now, now); err != nil {
			return err
		}

		_, err := tx.ExecContext(ctx, `
			INSERT INTO cloud_assets (
				id, binding_id, resource_key, source_type, state, generation,
				file_id, pick_code, file_name, size_bytes, container, parent_id,
				manifest_json, last_seen_at, created_at, updated_at
			) VALUES (?, ?, ?, 'permanent', 'ready', 1, ?, ?, ?, ?, ?, ?, '[]', ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				pick_code = excluded.pick_code,
				file_name = excluded.file_name,
				size_bytes = excluded.size_bytes,
				state = 'ready',
				last_seen_at = excluded.last_seen_at,
				updated_at = excluded.updated_at;
		`, assetID, binding.ID, resourceKey, fileID, pickCode, fileName, sizeBytes,
			ext, parentID, now, now, now)
		return err
	})
	if err == nil {
		stats.AssetsCreated++
		affected[code] = true
	}
}
