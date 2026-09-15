package javdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mediavault/internal/db"
	"mediavault/internal/identity"
	"mediavault/internal/models"
	"time"

	"github.com/google/uuid"
)

type ScrapeResult struct {
	Status         string // success, partial, not_found, transient
	IsEnriched     int
	MagnetsAdded   int
	MagnetsUpdated int
	Error          error
}

type Scraper struct {
	database *db.DB
	client   *Client
	jobRepo  *db.JobRepo
	logger   *slog.Logger
}

func NewScraper(database *db.DB, client *Client, logger *slog.Logger) *Scraper {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scraper{
		database: database,
		client:   client,
		jobRepo:  db.NewJobRepo(database),
		logger:   logger,
	}
}

// ScrapeMovie performs complete scraping for a single movie code.
func (s *Scraper) ScrapeMovie(ctx context.Context, code string) (*ScrapeResult, error) {
	now := models.UTCNow()
	nowTime := time.Now().UTC()
	today := nowTime.Format("2006-01-02")

	// Read current movie state
	query := `
		SELECT code, category, first_seen_at, is_enriched, scrape_policy, scrape_status,
		       not_found_count, last_not_found_day, partial_attempts, manual_fields, metadata_sources
		FROM offline_movies
		WHERE code = ?;
	`
	row := s.database.Reader().QueryRowContext(ctx, query, code)

	var mCode, category, firstSeenAt, policy, status, manualJSON, metaSourcesJSON string
	var notFoundDay sql.NullString
	var isEnriched, notFoundCount, partialAttempts int

	err := row.Scan(
		&mCode, &category, &firstSeenAt, &isEnriched, &policy, &status,
		&notFoundCount, &notFoundDay, &partialAttempts, &manualJSON, &metaSourcesJSON,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("movie %s not found in db", code)
		}
		return nil, fmt.Errorf("read movie %s: %w", code, err)
	}

	// 1. Search JavDB
	searchRes, err := s.client.SearchMovie(ctx, code)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.handleNotFound(ctx, code, today, firstSeenAt, notFoundCount, notFoundDay, isEnriched, now)
			return &ScrapeResult{Status: "not_found", IsEnriched: isEnriched}, nil
		}
		if errors.Is(err, ErrRiskControl) || errors.Is(err, ErrTransient) || errors.Is(err, ErrCircuitOpen) {
			s.handleTransient(ctx, code, status, isEnriched, now, err)
			return &ScrapeResult{Status: "transient", IsEnriched: isEnriched, Error: err}, err
		}
		return nil, fmt.Errorf("search javdb %s: %w", code, err)
	}

	// 2. Fetch full details
	detail, err := s.client.GetMovieDetail(ctx, searchRes.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			s.handleNotFound(ctx, code, today, firstSeenAt, notFoundCount, notFoundDay, isEnriched, now)
			return &ScrapeResult{Status: "not_found", IsEnriched: isEnriched}, nil
		}
		s.handleTransient(ctx, code, status, isEnriched, now, err)
		return &ScrapeResult{Status: "transient", IsEnriched: isEnriched, Error: err}, err
	}

	// 3. Fetch magnets and review links
	magnets, _ := s.client.GetOfficialMagnets(ctx, searchRes.ID)
	reviewLinks, _ := s.client.GetReviewLinks(ctx, searchRes.ID)

	// 4. Merge details and magnets inside a single transaction
	var res ScrapeResult
	err = s.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		// Calculate completeness
		completeness, isSuccess := CalculateCompleteness(detail)
		res.IsEnriched = completeness

		var nextScrapeAt *string
		newPolicy := policy
		var policyReason *string
		var newStatus string

		if isSuccess {
			newStatus = "success"
			notFoundCount = 0
			partialAttempts = 0
		} else {
			newStatus = "partial"
			partialAttempts++
			if partialAttempts >= 5 {
				newPolicy = "paused"
				reason := "missing_fields"
				policyReason = &reason
			} else {
				t24 := nowTime.Add(24 * time.Hour).Format(time.RFC3339)
				nextScrapeAt = &t24
			}
		}

		res.Status = newStatus

		// Update movie fields respecting manual locks
		var manualFields []string
		_ = json.Unmarshal([]byte(manualJSON), &manualFields)
		isLocked := func(f string) bool {
			for _, m := range manualFields {
				if m == f {
					return true
				}
			}
			return false
		}

		actorsJSON, _ := json.Marshal(detail.Actors)
		tagsJSON, _ := json.Marshal(detail.Tags)

		var runtimeTicks *int64
		if detail.RuntimeSeconds != nil && *detail.RuntimeSeconds > 0 {
			ticks := int64(*detail.RuntimeSeconds) * 10000000
			runtimeTicks = &ticks
		}

		updateQuery := `
			UPDATE offline_movies
			SET title_zh = CASE WHEN ? AND ? != '' THEN ? ELSE title_zh END,
			    description_zh = CASE WHEN ? AND ? != '' THEN ? ELSE description_zh END,
			    cover_url = CASE WHEN ? AND ? != '' THEN ? ELSE cover_url END,
			    poster_url = CASE WHEN ? AND ? != '' THEN ? ELSE poster_url END,
			    official_title = CASE WHEN ? AND ? != '' THEN ? ELSE official_title END,
			    actors = CASE WHEN ? AND ? != '[]' THEN ? ELSE actors END,
			    tags = CASE WHEN ? AND ? != '[]' THEN ? ELSE tags END,
			    maker = CASE WHEN ? AND ? != '' THEN ? ELSE maker END,
			    director = CASE WHEN ? AND ? != '' THEN ? ELSE director END,
			    score = CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE score END,
			    release_date = CASE WHEN ? AND ? != '' THEN ? ELSE release_date END,
			    runtime_ticks = CASE WHEN ? AND ? IS NOT NULL THEN ? ELSE runtime_ticks END,
			    is_enriched = ?,
			    scrape_status = ?,
			    scrape_policy = ?,
			    policy_reason = ?,
			    partial_attempts = ?,
			    not_found_count = ?,
			    next_scrape_at = ?,
			    last_scraped_at = ?,
			    updated_at = ?
			WHERE code = ?;
		`

		titleZH := ""
		if detail.TitleZH != nil {
			titleZH = *detail.TitleZH
		}
		descZH := ""
		if detail.DescriptionZH != nil {
			descZH = *detail.DescriptionZH
		}
		cover := ""
		if detail.CoverURL != nil {
			cover = *detail.CoverURL
		}
		poster := ""
		if detail.PosterURL != nil {
			poster = *detail.PosterURL
		}
		offTitle := detail.Title
		maker := ""
		if detail.Maker != nil {
			maker = *detail.Maker
		}
		director := ""
		if detail.Director != nil {
			director = *detail.Director
		}
		relDate := ""
		if detail.ReleaseDate != nil {
			relDate = *detail.ReleaseDate
		}

		_, err = tx.ExecContext(ctx, updateQuery,
			!isLocked("title_zh"), titleZH, titleZH,
			!isLocked("description_zh"), descZH, descZH,
			!isLocked("cover_url"), cover, cover,
			!isLocked("poster_url"), poster, poster,
			!isLocked("official_title"), offTitle, offTitle,
			!isLocked("actors"), string(actorsJSON), string(actorsJSON),
			!isLocked("tags"), string(tagsJSON), string(tagsJSON),
			!isLocked("maker"), maker, maker,
			!isLocked("director"), director, director,
			!isLocked("score"), detail.Score, detail.Score,
			!isLocked("release_date"), relDate, relDate,
			!isLocked("runtime_ticks"), runtimeTicks, runtimeTicks,
			completeness, newStatus, newPolicy, policyReason,
			partialAttempts, notFoundCount, nextScrapeAt, now, now, code,
		)
		if err != nil {
			return fmt.Errorf("update movie tx: %w", err)
		}

		// Insert / merge official magnets
		for _, m := range magnets {
			added, updated, err := s.mergeScrapedMagnet(ctx, tx, code, m.MagnetURL, m.Name, m.SizeBytes, "javdb", now)
			if err != nil {
				continue
			}
			if added {
				res.MagnetsAdded++
			} else if updated {
				res.MagnetsUpdated++
			}
		}

		// Insert review links
		for _, link := range reviewLinks {
			added, updated, err := s.mergeScrapedMagnet(ctx, tx, code, link, "", 0, "javdb_review", now)
			if err != nil {
				continue
			}
			if added {
				res.MagnetsAdded++
			} else if updated {
				res.MagnetsUpdated++
			}
		}

		// Recompute preferred magnet
		if err := db.RecomputePreferredMagnetTx(ctx, tx, code); err != nil {
			return fmt.Errorf("recompute preferred magnet: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &res, nil
}

func (s *Scraper) mergeScrapedMagnet(ctx context.Context, tx *sql.Tx, movieCode, uri, title string, sizeBytes int64, website, now string) (added bool, updated bool, err error) {
	kind, key, err := identity.ParseResourceURI(uri)
	if err != nil || kind == identity.KindUnsupported {
		return false, false, nil
	}

	quality := identity.ExtractQualityFlags(title)
	qualityLabel := "1080P"
	if quality.Is4K == 1 {
		qualityLabel = "4K"
	}

	// Check if magnet exists
	var existingCode string
	err = tx.QueryRowContext(ctx, "SELECT movie_code FROM offline_magnets WHERE info_hash = ?", key).Scan(&existingCode)
	if err == sql.ErrNoRows {
		// New magnet
		_, err = tx.ExecContext(ctx, `
			INSERT INTO offline_magnets (
				info_hash, movie_code, resource_kind, magnet_url, title, size_bytes,
				quality_label, website, has_chinese_sub, is_cracked, is_4k, is_censored,
				is_preferred, enabled, metadata_sources, manual_fields, legacy_runtime,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 1, '{}', '[]', '{}', ?, ?)
		`, key, movieCode, string(kind), uri, title, sizeBytes, qualityLabel, website,
			quality.HasChineseSub, quality.IsCracked, quality.Is4K, quality.IsCensored, now, now)
		if err != nil {
			return false, false, err
		}
		return true, false, nil
	} else if err != nil {
		return false, false, err
	}

	// If existing belongs to different movie, cross-movie conflict! Do not reassign.
	if existingCode != movieCode {
		return false, false, nil
	}

	// Same movie: update size if incoming has size and existing is 0
	_, err = tx.ExecContext(ctx, `
		UPDATE offline_magnets
		SET size_bytes = CASE WHEN size_bytes = 0 AND ? > 0 THEN ? ELSE size_bytes END,
		    updated_at = ?
		WHERE info_hash = ?
	`, sizeBytes, sizeBytes, now, key)
	if err != nil {
		return false, false, err
	}

	return false, true, nil
}

func (s *Scraper) handleNotFound(ctx context.Context, code, today, firstSeenAt string, notFoundCount int, lastNotFoundDay sql.NullString, isEnriched int, now string) {
	nowTime := time.Now().UTC()
	nextNotFoundCount := notFoundCount
	if !lastNotFoundDay.Valid || lastNotFoundDay.String != today {
		nextNotFoundCount++
	}

	// Check if firstSeenAt >= 10 days
	var isOver10Days bool
	firstSeenTime, err := time.Parse(time.RFC3339, firstSeenAt)
	if err == nil && nowTime.Sub(firstSeenTime) >= 10*24*time.Hour {
		isOver10Days = true
	}

	var newPolicy string
	var policyReason *string
	var nextScrapeAt *string

	if nextNotFoundCount >= 3 && isOver10Days {
		newPolicy = "paused"
		reason := "not_found"
		policyReason = &reason
	} else {
		newPolicy = "auto"
		t24 := nowTime.Add(24 * time.Hour).Format(time.RFC3339)
		nextScrapeAt = &t24
	}

	query := `
		UPDATE offline_movies
		SET scrape_status = 'not_found',
		    not_found_count = ?,
		    last_not_found_day = ?,
		    scrape_policy = ?,
		    policy_reason = ?,
		    next_scrape_at = ?,
		    last_scraped_at = ?,
		    updated_at = ?
		WHERE code = ?;
	`
	_ = s.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, nextNotFoundCount, today, newPolicy, policyReason, nextScrapeAt, now, now, code)
		return err
	})
}

func (s *Scraper) handleTransient(ctx context.Context, code, lastStatus string, isEnriched int, now string, lastErr error) {
	// Transient backoff: 1m -> 5m -> 30m -> 2h -> 6h
	backoff := 1 * time.Minute
	switch lastStatus {
	case "transient":
		backoff = 5 * time.Minute
	}

	nextScrapeAt := time.Now().UTC().Add(backoff).Format(time.RFC3339)
	errMsg := lastErr.Error()

	query := `
		UPDATE offline_movies
		SET scrape_status = 'transient',
		    scrape_failed_reason = ?,
		    next_scrape_at = ?,
		    updated_at = ?
		WHERE code = ?;
	`
	_ = s.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, query, errMsg, nextScrapeAt, now, code)
		return err
	})
}

// ScrapeBatchParams controls a batch scraping run triggered from the admin console.
type ScrapeBatchParams struct {
	DateField     string `json:"date_field"`
	StartDate     string `json:"start_date"`
	EndDate       string `json:"end_date"`
	IncludeFailed bool   `json:"include_failed"`
	IncludeExempt bool   `json:"include_exempt"`
	Limit         int    `json:"limit"`
	OnlyIdle      bool   `json:"only_idle"` // only never-scraped (newly ingested) movies
}

// ScrapeBatchStats summarizes a completed batch scraping run.
type ScrapeBatchStats struct {
	Total     int `json:"total"`
	Processed int `json:"processed"`
	Succeeded int `json:"succeeded"`
	Partial   int `json:"partial"`
	NotFound  int `json:"not_found"`
	Transient int `json:"transient"`
	Failed    int `json:"failed"`
}

// ScrapeBatch creates a scrape job, claims it and processes candidate movies in the
// background. It reuses an already active scrape job instead of double-scheduling.
func (s *Scraper) ScrapeBatch(ctx context.Context, params ScrapeBatchParams) (*models.Job, int, error) {
	dateField := "release_date"
	if params.DateField == "publish_date" {
		dateField = "publish_date"
	}
	limit := params.Limit
	if limit <= 0 {
		limit = 500
	}

	codes, err := s.listCandidates(ctx, dateField, params, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("list scrape candidates: %w", err)
	}

	paramsJSON, _ := json.Marshal(params)
	job, created, err := s.jobRepo.CreateOrGetJob(ctx, &models.Job{
		Kind:       "scrape",
		DedupeKey:  "scrape_batch",
		ParamsJSON: string(paramsJSON),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("create scrape job: %w", err)
	}
	if !created && (job.State == "running" || job.State == "queued" || job.State == "reconcile") {
		return job, len(codes), nil
	}

	workerID := "scrape-worker-" + uuid.New().String()[:8]
	claimed, err := s.jobRepo.ClaimJobByID(ctx, job.ID, workerID, 6*time.Hour)
	if err != nil || claimed == nil {
		return job, len(codes), nil
	}

	go s.executeScrapeBatch(context.Background(), claimed, codes)
	return claimed, len(codes), nil
}

func (s *Scraper) listCandidates(ctx context.Context, dateField string, params ScrapeBatchParams, limit int) ([]string, error) {
	where := "deleted_at IS NULL"
	var args []interface{}

	if !params.IncludeExempt {
		where += " AND scrape_policy != 'exempt'"
	}
	// Candidates are movies that have not been successfully scraped yet
	// (newly ingested resources), plus retryable failures.
	if params.OnlyIdle {
		where += " AND scrape_status = 'idle'"
	} else if params.IncludeFailed {
		where += " AND (scrape_status IN ('idle', 'transient', 'partial', 'not_found'))"
	} else {
		where += " AND scrape_status NOT IN ('success')"
	}
	if params.StartDate != "" {
		where += fmt.Sprintf(" AND %s >= ?", dateField)
		args = append(args, params.StartDate)
	}
	if params.EndDate != "" {
		where += fmt.Sprintf(" AND %s <= ?", dateField)
		args = append(args, params.EndDate)
	}
	args = append(args, limit)

	var codes []string
	err := s.database.ExecRead(ctx, func(d *sql.DB) error {
		query := fmt.Sprintf("SELECT code FROM offline_movies WHERE %s ORDER BY updated_at ASC LIMIT ?", where)
		rows, err := d.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code string
			if err := rows.Scan(&code); err != nil {
				return err
			}
			codes = append(codes, code)
		}
		return rows.Err()
	})
	return codes, err
}

func (s *Scraper) executeScrapeBatch(ctx context.Context, job *models.Job, codes []string) {
	s.logger.Info("开始后台批量刮削", "任务", job.ID, "候选数", len(codes))

	stats := ScrapeBatchStats{Total: len(codes)}
	writeProgress := func() {
		resBytes, _ := json.Marshal(stats)
		_ = s.jobRepo.UpdateJobResult(ctx, job.ID, string(resBytes))
	}
	writeProgress() // publish the total immediately

	for i, code := range codes {
		res, err := s.ScrapeMovie(ctx, code)
		stats.Processed = i + 1
		if err != nil && res == nil {
			stats.Failed++
			// Risk control / circuit open: stop early to avoid escalating a ban.
			if errors.Is(err, ErrRiskControl) || errors.Is(err, ErrCircuitOpen) {
				s.logger.Warn("刮削中断：触发风控或熔断", "任务", job.ID, "影片", code, "错误", err.Error())
				writeProgress()
				break
			}
			s.logger.Warn("刮削失败", "影片", code, "错误", err.Error())
		} else {
			switch res.Status {
			case "success":
				stats.Succeeded++
			case "partial":
				stats.Partial++
			case "not_found":
				stats.NotFound++
			case "transient":
				stats.Transient++
			default:
				stats.Failed++
			}
			s.logger.Info("刮削条目完成", "影片", code, "结果", res.Status, "进度", fmt.Sprintf("%d/%d", i+1, len(codes)))
		}

		// Publish progress after every item so the console updates live.
		writeProgress()
		if (i+1)%10 == 0 || i+1 == len(codes) {
			s.logger.Info("刮削进度",
				"已处理", i+1, "共", len(codes),
				"成功", stats.Succeeded, "部分", stats.Partial,
				"未找到", stats.NotFound, "临时失败", stats.Transient, "失败", stats.Failed)
		}
	}

	resBytes, _ := json.Marshal(stats)
	_ = s.jobRepo.FinishJob(ctx, job.ID, "succeeded", string(resBytes), nil)
	s.logger.Info("后台批量刮削完成",
		"任务", job.ID,
		"成功", stats.Succeeded, "部分", stats.Partial,
		"未找到", stats.NotFound, "临时失败", stats.Transient, "失败", stats.Failed)
}
