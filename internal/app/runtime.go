// Package app wires together runtime services and exposes task-trigger entry points
// used by the admin API. It exists so the HTTP layer does not need to know how the
// background workers are constructed.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"mediavault/internal/client115"
	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/ingestion"
	"mediavault/internal/javdb"
	"mediavault/internal/models"
	"mediavault/internal/services"

	"github.com/google/uuid"
)

// Runtime owns long-lived service instances and background workers.
type Runtime struct {
	cfg      *config.AppConfig
	logger   *slog.Logger
	settings *db.SettingsRepo
	jobRepo  *db.JobRepo

	C115    *client115.Client
	Auth115 *client115.AuthClient
	JavDB   *javdb.Client

	Scraper   *javdb.Scraper
	Rankings  *javdb.RankingsService
	Ingestion *ingestion.IngestionService
	Scanner   *services.TreeScanner
}

// New builds a Runtime from already-initialized shared dependencies.
func New(
	cfg *config.AppConfig,
	logger *slog.Logger,
	database *db.DB,
	settingsRepo *db.SettingsRepo,
	movieRepo *db.MovieRepo,
	magnetRepo *db.MagnetRepo,
	assetRepo *db.AssetRepo,
	jobRepo *db.JobRepo,
	c115 *client115.Client,
	auth115 *client115.AuthClient,
) *Runtime {
	javdbClient := javdb.NewClient(javdb.ClientConfig{})
	scraper := javdb.NewScraper(database, javdbClient, logger)
	rankings := javdb.NewRankingsService(database, movieRepo, jobRepo, javdbClient, scraper, logger)

	dlDir := cfg.DataDir + "/downloads"
	ingestRepo := db.NewIngestRepo(database)
	ingestionSvc := ingestion.NewIngestionService(database, jobRepo, ingestRepo, ingestion.IngestionConfig{
		DownloadDir:             dlDir,
		PasswordDigest:          ingestion.DefaultResourceLibraryPasswordDigest,
		ArchiveMaxBytes:         512 * 1024 * 1024,
		ArchiveExpandedMaxBytes: 4 * 1024 * 1024 * 1024,
		PBKDF2MaxIterations:     1000000,
	}, logger)

	scanner := services.NewTreeScanner(database, assetRepo, movieRepo, magnetRepo, jobRepo, c115, logger)

	return &Runtime{
		cfg:       cfg,
		logger:    logger,
		settings:  settingsRepo,
		jobRepo:   jobRepo,
		C115:      c115,
		Auth115:   auth115,
		JavDB:     javdbClient,
		Scraper:   scraper,
		Rankings:  rankings,
		Ingestion: ingestionSvc,
		Scanner:   scanner,
	}
}

// applyJavDBToken loads the optional JavDB account token from settings so that
// ranking/scrape requests can use authenticated endpoints when configured.
func (r *Runtime) applyJavDBToken(ctx context.Context) {
	if r.JavDB == nil || r.cfg == nil {
		return
	}
	token, err := r.settings.GetDecryptedSecret(ctx, r.cfg.MasterKey, "javdb_token")
	if err != nil {
		r.logger.Warn("failed to read javdb_token secret", "error", err)
		return
	}
	r.JavDB.SetToken(token)
	// Session cookie (captured at login) is used alongside the bearer token.
	if cookie, cerr := r.settings.GetDecryptedSecret(ctx, r.cfg.MasterKey, "javdb_cookie"); cerr == nil && cookie != "" {
		r.JavDB.SetCookie(cookie)
	}
}

// TriggerRankings synchronizes a JavDB ranking board.
func (r *Runtime) TriggerRankings(ctx context.Context, period, rankingType, year string, limit int) (string, error) {
	r.applyJavDBToken(ctx)
	if rankingType == "" {
		rankingType = "0"
	}

	periods := []string{period}
	if period == "" || period == "all" {
		// Sync all three boards used by the virtual libraries (周榜/月榜/TOP250).
		periods = []string{"weekly", "monthly", "top250"}
	}

	firstID := ""
	var lastErr error
	for _, p := range periods {
		job, err := r.Rankings.SyncRankings(ctx, javdb.SyncRankingsParams{
			Period:      p,
			RankingType: rankingType,
			Year:        year,
			Limit:       limit,
		})
		if err != nil {
			lastErr = err
			r.logger.Warn("榜单同步提交失败", "榜单", p, "错误", err.Error())
			continue
		}
		if job != nil && firstID == "" {
			firstID = job.ID
		}
	}
	if firstID == "" && lastErr != nil {
		return "", lastErr
	}
	return firstID, nil
}

// TriggerScrape runs a batch JavDB scrape over local candidates.
func (r *Runtime) TriggerScrape(ctx context.Context, dateField, startDate, endDate string, includeFailed, includeExempt bool, limit int) (string, int, error) {
	r.applyJavDBToken(ctx)
	params := javdb.ScrapeBatchParams{
		DateField:     dateField,
		StartDate:     startDate,
		EndDate:       endDate,
		IncludeFailed: includeFailed,
		IncludeExempt: includeExempt,
		Limit:         limit,
	}
	job, count, err := r.Scraper.ScrapeBatch(ctx, params)
	if err != nil {
		return "", 0, err
	}
	if job == nil {
		return "", 0, nil
	}
	return job.ID, count, nil
}

// TriggerSync30D triggers the incremental AVDB 30-day import and, once it
// finishes successfully, automatically scrapes the newly ingested movies.
func (r *Runtime) TriggerSync30D(ctx context.Context) (string, error) {
	job, err := r.Ingestion.Sync30D(ctx)
	if err != nil {
		return "", err
	}
	if job == nil {
		return "", nil
	}
	go r.watchJobThenScrape(context.Background(), job.ID, "30天增量同步")
	return job.ID, nil
}

// TriggerScrapeNew scrapes only never-scraped (newly ingested) movies.
func (r *Runtime) TriggerScrapeNew(ctx context.Context) (string, int, error) {
	r.applyJavDBToken(ctx)
	job, count, err := r.Scraper.ScrapeBatch(ctx, javdb.ScrapeBatchParams{OnlyIdle: true})
	if err != nil {
		return "", 0, err
	}
	if job == nil {
		return "", 0, nil
	}
	return job.ID, count, nil
}

// watchJobThenScrape waits for an ingestion job to finish, then scrapes the
// newly ingested movies (linked automation for the sync30d schedule).
func (r *Runtime) watchJobThenScrape(ctx context.Context, jobID, label string) {
	for i := 0; i < 2160; i++ { // up to 6h at 10s interval
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
		j, err := r.jobRepo.GetJobByID(ctx, jobID)
		if err != nil || j == nil {
			return
		}
		switch j.State {
		case "succeeded":
			r.logger.Info(label+"完成，开始刮削新入库番号", "任务", jobID)
			if id, count, err := r.TriggerScrapeNew(ctx); err != nil {
				r.logger.Warn("联动刮削失败", "错误", err.Error())
			} else {
				r.logger.Info("已提交增量刮削任务", "任务", id, "候选数", count)
			}
			return
		case "failed", "cancelled":
			r.logger.Warn(label+"未成功，跳过联动刮削", "任务", jobID, "状态", j.State)
			return
		}
	}
}

// TriggerImportFull triggers the full AVDB library import.
func (r *Runtime) TriggerImportFull(ctx context.Context) (string, error) {
	job, err := r.Ingestion.ImportFull(ctx)
	if err != nil {
		return "", err
	}
	if job == nil {
		return "", nil
	}
	return job.ID, nil
}

// TriggerScan reconciles permanent 115 assets under the configured scan roots.
func (r *Runtime) TriggerScan(ctx context.Context, rootCID, mode string) (string, error) {
	// Apply the optional 115 web cookie so tree traversal uses the cookie web API,
	// which is far more reliable for bulk directory listing than the OpenAPI.
	if cookie, err := r.settings.GetDecryptedSecret(ctx, r.cfg.MasterKey, "115_cookie"); err == nil && cookie != "" {
		r.C115.SetCookie(cookie)
	}
	if rootCID == "" {
		roots := r.scanRoots(ctx)
		if len(roots) == 0 {
			return "", fmt.Errorf("未配置扫描根目录 (existing_scan_cids)，且未指定 root_cid")
		}
		return r.triggerScanRoots(ctx, roots, mode)
	}
	return r.triggerScanRoots(ctx, []string{rootCID}, mode)
}

// triggerScanRoots runs a scan over every configured root in one background job.
func (r *Runtime) triggerScanRoots(ctx context.Context, roots []string, mode string) (string, error) {
	if mode == "" {
		mode = "incremental"
	}

	paramsBytes, _ := json.Marshal(map[string]interface{}{"roots": roots, "mode": mode})
	job, created, err := r.jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:         "job_scan_" + uuid.New().String()[:8],
		Kind:       "scan",
		DedupeKey:  "scan:all",
		State:      "queued",
		Generation: 1,
		ParamsJSON: string(paramsBytes),
		ResultJSON: "{}",
	})
	if err != nil {
		return "", err
	}
	if !created && (job.State == "running" || job.State == "queued") {
		return job.ID, nil
	}

	workerID := "scan-worker-" + uuid.New().String()[:8]
	claimed, err := r.jobRepo.ClaimJobByID(ctx, job.ID, workerID, 24*time.Hour)
	if err != nil || claimed == nil {
		return job.ID, nil
	}

	go func() {
		// A panic here must never take down the whole server.
		defer func() {
			if rec := recover(); rec != nil {
				errMsg := fmt.Sprintf("扫描任务异常: %v", rec)
				r.logger.Error("扫描任务发生 panic", "错误", errMsg)
				_ = r.jobRepo.FinishJob(context.Background(), claimed.ID, "failed", "{}", &errMsg)
			}
		}()

		bg := context.Background()
		result := map[string]interface{}{"roots": len(roots), "succeeded": 0, "failed": 0, "assets": 0}
		var lastErr string
		for _, root := range roots {
			stats, err := r.Scanner.ScanCID(bg, root, mode)
			if err != nil {
				result["failed"] = result["failed"].(int) + 1
				lastErr = err.Error()
				r.logger.Warn("扫描根目录失败", "根目录CID", root, "错误", err.Error())
				continue
			}
			result["succeeded"] = result["succeeded"].(int) + 1
			if stats != nil {
				result["assets"] = result["assets"].(int) + stats.AssetsCreated
			}
		}
		resBytes, _ := json.Marshal(result)
		if lastErr != "" && result["succeeded"].(int) == 0 {
			_ = r.jobRepo.FinishJob(bg, claimed.ID, "failed", string(resBytes), &lastErr)
			return
		}
		_ = r.jobRepo.FinishJob(bg, claimed.ID, "succeeded", string(resBytes), nil)
	}()

	return claimed.ID, nil
}

func (r *Runtime) scanRoots(ctx context.Context) []string {
	s, err := r.settings.GetSetting(ctx, "existing_scan_cids")
	if err != nil || s == nil || s.Value == nil || *s.Value == "" {
		return nil
	}
	var roots []string
	if json.Unmarshal([]byte(*s.Value), &roots) != nil {
		return nil
	}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) != "" {
			out = append(out, strings.TrimSpace(root))
		}
	}
	return out
}

func (r *Runtime) firstScanRoot(ctx context.Context) string {
	s, err := r.settings.GetSetting(ctx, "existing_scan_cids")
	if err != nil || s == nil || s.Value == nil || *s.Value == "" {
		return ""
	}
	var roots []string
	if json.Unmarshal([]byte(*s.Value), &roots) == nil && len(roots) > 0 {
		return roots[0]
	}
	return ""
}

// StartupDelay is exposed so main can stagger optional background workers.
const StartupDelay = 2 * time.Second

// Start launches the background schedule executor. It is safe to call once.
func (r *Runtime) Start(ctx context.Context) {
	go r.runScheduler(ctx)
}

type scheduleRule struct {
	Type    string `json:"type"`    // daily, weekly, interval
	Hour    int    `json:"hour"`    // 0-23
	Minute  int    `json:"minute"`  // 0-59
	Weekday int    `json:"weekday"` // 1-7 (Monday=1)
	Minutes int    `json:"minutes"` // interval in minutes
}

func (r *Runtime) runScheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// small initial delay to let startup settle
	time.Sleep(StartupDelay)
	r.logger.Info("schedule executor started")

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.evaluateSchedules(ctx)
		}
	}
}

func (r *Runtime) evaluateSchedules(ctx context.Context) {
	schedules, err := r.settings.ListSchedules(ctx)
	if err != nil {
		r.logger.Warn("schedule executor: list schedules failed", "error", err)
		return
	}

	for i := range schedules {
		s := schedules[i]
		if s.Enabled != 1 {
			continue
		}

		var rule scheduleRule
		if err := json.Unmarshal([]byte(s.RuleJSON), &rule); err != nil {
			continue
		}

		loc, err := time.LoadLocation(s.Timezone)
		if err != nil {
			loc = time.FixedZone("CST", 8*3600)
		}
		now := time.Now().In(loc)

		slot, next, ok := mostRecentSlot(rule, now)
		if !ok {
			continue
		}
		slotKey := slot.UTC().Format(time.RFC3339)
		if s.LastSlot != nil && *s.LastSlot == slotKey {
			continue
		}

		// Do not backfill slots older than 10 minutes.
		if now.Sub(slot) > 10*time.Minute {
			s.LastSlot = &slotKey
			nextStr := next.UTC().Format(time.RFC3339)
			s.NextRunAt = &nextStr
			_ = r.settings.UpsertSchedule(ctx, &s)
			continue
		}

		r.triggerSchedule(ctx, s)

		s.LastSlot = &slotKey
		nextStr := next.UTC().Format(time.RFC3339)
		s.NextRunAt = &nextStr
		if err := r.settings.UpsertSchedule(ctx, &s); err != nil {
			r.logger.Warn("schedule executor: persist schedule state failed", "error", err)
		}
	}
}

func (r *Runtime) triggerSchedule(ctx context.Context, s models.Schedule) {
	r.logger.Info("schedule executor triggering task", "schedule_id", s.ID, "kind", s.Kind)
	switch s.Kind {
	case "sync30d":
		if _, err := r.TriggerSync30D(ctx); err != nil {
			r.logger.Warn("scheduled sync30d failed", "error", err)
		}
	case "rankings":
		var params struct {
			Board string `json:"board"`
			Year  string `json:"year"`
		}
		_ = json.Unmarshal([]byte(s.ParamsJSON), &params)
		if _, err := r.TriggerRankings(ctx, "all", "0", params.Year, 0); err != nil {
			r.logger.Warn("scheduled rankings failed", "error", err)
		}
	case "scan":
		if _, err := r.TriggerScan(ctx, "", "incremental"); err != nil {
			r.logger.Warn("scheduled scan failed", "error", err)
		}
	}
}

// mostRecentSlot returns the most recent scheduled time at or before now, the next
// scheduled time, and whether the rule is a time-based rule.
func mostRecentSlot(rule scheduleRule, now time.Time) (time.Time, time.Time, bool) {
	switch rule.Type {
	case "daily":
		candidate := time.Date(now.Year(), now.Month(), now.Day(), rule.Hour, rule.Minute, 0, 0, now.Location())
		if candidate.After(now) {
			candidate = candidate.AddDate(0, 0, -1)
		}
		return candidate, candidate.AddDate(0, 0, 1), true
	case "weekly":
		// Go weekday: Sunday=0. Rule weekday: Monday=1..Sunday=7.
		target := rule.Weekday % 7 // Sunday(7)->0, Monday(1)->1 ...
		candidate := time.Date(now.Year(), now.Month(), now.Day(), rule.Hour, rule.Minute, 0, 0, now.Location())
		daysBack := (int(now.Weekday()) - target + 7) % 7
		candidate = candidate.AddDate(0, 0, -daysBack)
		if candidate.After(now) {
			candidate = candidate.AddDate(0, 0, -7)
		}
		return candidate, candidate.AddDate(0, 0, 7), true
	default:
		return time.Time{}, time.Time{}, false
	}
}

// Login authenticates against JavDB with a username/password and persists the
// resulting bearer token (encrypted) so it survives restarts.
func (r *Runtime) Login(ctx context.Context, username, password string) (string, error) {
	res, err := r.JavDB.Login(ctx, username, password)
	if err != nil {
		return "", err
	}
	if r.cfg != nil && r.cfg.MasterKey != nil {
		secrets := map[string]string{}
		if env, encErr := r.cfg.MasterKey.Encrypt("javdb_token", []byte(res.Token)); encErr == nil {
			secrets["javdb_token"] = env
		}
		if cookie := r.JavDB.GetCookie(); cookie != "" {
			if env, encErr := r.cfg.MasterKey.Encrypt("javdb_cookie", []byte(cookie)); encErr == nil {
				secrets["javdb_cookie"] = env
			}
		}
		if len(secrets) > 0 {
			if _, err := r.settings.UpdateSettings(ctx, 0, nil, secrets, nil); err != nil {
				r.logger.Warn("failed to persist javdb credentials", "error", err)
			}
		}
	}
	name := res.Username
	if name == "" {
		name = username
	}
	// Make sure background workers pick up the freshly stored token.
	r.applyJavDBToken(ctx)
	return name, nil
}

// Logout clears the JavDB token from memory and from persisted settings.
func (r *Runtime) Logout(ctx context.Context) error {
	r.JavDB.SetToken("")
	r.JavDB.SetCookie("")
	_, err := r.settings.UpdateSettings(ctx, 0, nil, nil, []string{"javdb_token", "javdb_cookie"})
	return err
}

// JavDBLoggedIn reports whether a JavDB account token is currently stored.
func (r *Runtime) JavDBLoggedIn(ctx context.Context) bool {
	s, err := r.settings.GetSetting(ctx, "javdb_token")
	return err == nil && s != nil && s.Value != nil && *s.Value != ""
}
