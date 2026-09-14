package ingestion

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mediavault/internal/db"
	"mediavault/internal/models"
	"os"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSyncAlreadyRunning = errors.New("sync or import job is already running")
	ErrSyncGapDetected    = errors.New("sync gap greater than 45 days detected, full import required")
)

type IngestionConfig struct {
	GitHubAPIURL            string
	Mirrors                 []string
	ArchiveMaxBytes         int64
	ArchiveExpandedMaxBytes int64
	PBKDF2MaxIterations     int
	DownloadDir             string
	PasswordDigest          []byte
}

type IngestionService struct {
	database   *db.DB
	jobRepo    *db.JobRepo
	ingestRepo *db.IngestRepo
	downloader *Downloader
	cfg        IngestionConfig
	logger     *slog.Logger
}

func NewIngestionService(
	database *db.DB,
	jobRepo *db.JobRepo,
	ingestRepo *db.IngestRepo,
	cfg IngestionConfig,
	logger *slog.Logger,
) *IngestionService {
	if logger == nil {
		logger = slog.Default()
	}
	dl := NewDownloader(nil, cfg.GitHubAPIURL, cfg.Mirrors, cfg.ArchiveMaxBytes, cfg.DownloadDir)
	return &IngestionService{
		database:   database,
		jobRepo:    jobRepo,
		ingestRepo: ingestRepo,
		downloader: dl,
		cfg:        cfg,
		logger:     logger,
	}
}

// Sync30D triggers the daily incremental sync for both 30D sources.
func (s *IngestionService) Sync30D(ctx context.Context) (*models.Job, error) {
	return s.runIngestion(ctx, "sync30d", []string{"30D_sehuatang", "30D_X1080X"})
}

// ImportFull triggers the full database import for both All sources.
func (s *IngestionService) ImportFull(ctx context.Context) (*models.Job, error) {
	return s.runIngestion(ctx, "import_full", []string{"All_sehuatang", "All_X1080X"})
}

func (s *IngestionService) runIngestion(ctx context.Context, kind string, sourcePrefixes []string) (*models.Job, error) {
	s.logger.Info("starting ingestion run", "kind", kind, "sources", sourcePrefixes)

	// 1. Check release info from GitHub
	release, err := s.downloader.FetchLatestRelease(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch release info: %w", err)
	}

	dedupeKey := fmt.Sprintf("%s:%s", kind, release.TagName)

	// 2. Create or claim job with mutual exclusion
	job, created, err := s.jobRepo.CreateOrGetJob(ctx, &models.Job{
		Kind:       kind,
		DedupeKey:  dedupeKey,
		ParamsJSON: fmt.Sprintf(`{"release":"%s"}`, release.TagName),
	})
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	if !created && (job.State == "running" || job.State == "queued" || job.State == "reconcile") {
		return nil, ErrSyncAlreadyRunning
	}

	// Claim lease
	workerID := "ingest-worker-" + uuid.New().String()[:8]
	claimed, err := s.jobRepo.ClaimJobByID(ctx, job.ID, workerID, 7200*time.Second)
	if err != nil || claimed == nil {
		return nil, fmt.Errorf("claim job lease: %w", err)
	}
	job = claimed

	// Execute ingestion asynchronously or synchronously depending on caller
	go func() {
		bgCtx := context.Background()
		s.executeIngestJob(bgCtx, job, release, sourcePrefixes)
	}()

	return job, nil
}

func (s *IngestionService) executeIngestJob(ctx context.Context, job *models.Job, release *ReleaseInfo, sourcePrefixes []string) {
	overallStats := BatchStats{}
	var hasError error

	// Keep lease alive in background
	leaseStop := make(chan struct{})
	defer close(leaseStop)
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-leaseStop:
				return
			case <-ticker.C:
				_ = s.jobRepo.RenewLease(context.Background(), job.ID, *job.LeaseOwner, job.Generation, 7200*time.Second)
			}
		}
	}()

	for _, prefix := range sourcePrefixes {
		s.logger.Info("downloading source asset", "job_id", job.ID, "prefix", prefix)
		downloaded, err := s.downloader.DownloadAsset(ctx, release, prefix)
		if err != nil {
			s.logger.Error("download asset failed", "prefix", prefix, "error", err)
			hasError = err
			break
		}

		// Record ingest_asset in DB
		assetID := uuid.New().String()
		ingestAsset := &models.IngestAsset{
			ID:            assetID,
			RunID:         job.ID,
			ReleaseID:     release.TagName,
			Source:        prefix,
			AssetName:     downloaded.AssetName,
			SHA256:        downloaded.SHA256,
			SizeBytes:     downloaded.SizeBytes,
			CoverageStart: downloaded.CoverageStart,
			CoverageEnd:   downloaded.CoverageEnd,
			State:         "downloaded",
		}
		if err := s.ingestRepo.UpsertIngestAsset(ctx, ingestAsset); err != nil {
			s.logger.Error("upsert ingest asset failed", "error", err)
			hasError = err
			break
		}

		// Process the downloaded file
		stats, err := s.processDownloadedFile(ctx, ingestAsset, downloaded.FilePath)
		if err != nil {
			s.logger.Error("process file failed", "prefix", prefix, "error", err)
			errMsg := err.Error()
			_ = s.ingestRepo.UpdateIngestAssetState(ctx, assetID, "failed", 0, "{}", &errMsg)
			hasError = err
			break
		}

		// Accumulate stats
		overallStats.TotalProcessed += stats.TotalProcessed
		overallStats.MoviesInserted += stats.MoviesInserted
		overallStats.MoviesUpdated += stats.MoviesUpdated
		overallStats.MagnetsInserted += stats.MagnetsInserted
		overallStats.MagnetsUpdated += stats.MagnetsUpdated
		overallStats.Conflicts = append(overallStats.Conflicts, stats.Conflicts...)
	}

	resultJSON, _ := json.Marshal(overallStats)

	if hasError != nil {
		errMsg := hasError.Error()
		_ = s.jobRepo.FinishJob(ctx, job.ID, "failed", string(resultJSON), &errMsg)
		return
	}

	// Verify all required sources succeeded
	allDone, err := s.ingestRepo.AreReleaseAssetsCompleted(ctx, release.TagName, sourcePrefixes)
	if err != nil || !allDone {
		errMsg := "some required sources did not complete"
		_ = s.jobRepo.FinishJob(ctx, job.ID, "failed", string(resultJSON), &errMsg)
		return
	}

	// Advance Watermark
	var covDate string
	if len(sourcePrefixes) > 0 {
		_, cov := extractCoverage(release.TagName, release.TagName)
		if cov != nil {
			covDate = *cov
		} else {
			covDate = time.Now().UTC().Format("2006-01-02")
		}
	}

	// Watermark gap check (45 days)
	existingWm, _ := s.ingestRepo.GetSyncWatermark(ctx)
	isGap := false
	if existingWm != nil && existingWm.CoverageDate != "" {
		lastTime, err1 := time.Parse("2006-01-02", existingWm.CoverageDate)
		currTime, err2 := time.Parse("2006-01-02", covDate)
		if err1 == nil && err2 == nil && currTime.Sub(lastTime) > 45*24*time.Hour {
			isGap = true
			s.logger.Warn("sync gap > 45 days detected between watermarks", "last", existingWm.CoverageDate, "current", covDate)
		}
	}

	wm := &db.SyncWatermark{
		ReleaseID:    release.TagName,
		CoverageDate: covDate,
		Sources:      sourcePrefixes,
		SyncGap:      isGap,
	}
	if err := s.ingestRepo.UpdateSyncWatermark(ctx, wm); err != nil {
		s.logger.Error("failed to update sync watermark", "error", err)
	}

	_ = s.jobRepo.FinishJob(ctx, job.ID, "succeeded", string(resultJSON), nil)
	s.logger.Info("ingest job completed successfully", "job_id", job.ID, "stats", overallStats)
}

// ProcessDownloadedFile handles decryption, parsing, and batch ingestion.
func (s *IngestionService) processDownloadedFile(ctx context.Context, asset *models.IngestAsset, filePath string) (*BatchStats, error) {
	_ = s.ingestRepo.UpdateIngestAssetState(ctx, asset.ID, "processing", 0, "{}", nil)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read file %s: %w", filePath, err)
	}

	// Decrypt
	decryptCfg := DecryptConfig{
		PasswordDigest:          s.cfg.PasswordDigest,
		ArchiveMaxBytes:         s.cfg.ArchiveMaxBytes,
		ArchiveExpandedMaxBytes: s.cfg.ArchiveExpandedMaxBytes,
		PBKDF2MaxIterations:     s.cfg.PBKDF2MaxIterations,
	}
	if len(decryptCfg.PasswordDigest) == 0 {
		decryptCfg.PasswordDigest = DefaultResourceLibraryPasswordDigest
	}
	if decryptCfg.ArchiveMaxBytes <= 0 {
		decryptCfg.ArchiveMaxBytes = 512 * 1024 * 1024
	}
	if decryptCfg.ArchiveExpandedMaxBytes <= 0 {
		decryptCfg.ArchiveExpandedMaxBytes = 4 * 1024 * 1024 * 1024
	}
	if decryptCfg.PBKDF2MaxIterations <= 0 {
		decryptCfg.PBKDF2MaxIterations = 1000000
	}

	fname, csvBytes, err := DecryptAVDBArchive(data, decryptCfg)
	if err != nil {
		return nil, fmt.Errorf("decrypt archive: %w", err)
	}

	s.logger.Info("decrypted asset successfully", "filename", fname, "size", len(csvBytes))

	// Stream and batch merge in chunks of 1000
	stats := &BatchStats{}
	var batch []*IngestRecord
	batchSize := 1000
	rowCounter := 0

	err = ParseCSVStream(csvBytes, asset.Source, func(raw RawCSVRow) error {
		rec, err := ParseRawRow(raw)
		if err != nil || rec == nil {
			return nil
		}

		batch = append(batch, rec)
		rowCounter++

		if len(batch) >= batchSize {
			if err := s.flushBatch(ctx, batch, stats); err != nil {
				return err
			}
			batch = batch[:0]
			_ = s.ingestRepo.UpdateIngestAssetState(ctx, asset.ID, "processing", rowCounter, "{}", nil)
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("parse csv stream: %w", err)
	}

	// Flush remaining records
	if len(batch) > 0 {
		if err := s.flushBatch(ctx, batch, stats); err != nil {
			return nil, err
		}
	}

	countsBytes, _ := json.Marshal(stats)
	_ = s.ingestRepo.UpdateIngestAssetState(ctx, asset.ID, "completed", rowCounter, string(countsBytes), nil)

	return stats, nil
}

func (s *IngestionService) flushBatch(ctx context.Context, batch []*IngestRecord, overall *BatchStats) error {
	tx, err := s.database.Writer().BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin batch tx: %w", err)
	}
	defer tx.Rollback()

	batchStats, err := MergeBatchTx(ctx, tx, batch)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch tx: %w", err)
	}

	overall.TotalProcessed += batchStats.TotalProcessed
	overall.MoviesInserted += batchStats.MoviesInserted
	overall.MoviesUpdated += batchStats.MoviesUpdated
	overall.MagnetsInserted += batchStats.MagnetsInserted
	overall.MagnetsUpdated += batchStats.MagnetsUpdated
	overall.Conflicts = append(overall.Conflicts, batchStats.Conflicts...)

	return nil
}
