package javdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"mediavault/internal/db"
	"mediavault/internal/identity"
	"mediavault/internal/models"
	"time"
)

type RankingsService struct {
	database  *db.DB
	movieRepo *db.MovieRepo
	jobRepo   *db.JobRepo
	client    *Client
	scraper   *Scraper
	logger    *slog.Logger
}

func NewRankingsService(
	database *db.DB,
	movieRepo *db.MovieRepo,
	jobRepo *db.JobRepo,
	client *Client,
	scraper *Scraper,
	logger *slog.Logger,
) *RankingsService {
	if logger == nil {
		logger = slog.Default()
	}
	return &RankingsService{
		database:  database,
		movieRepo: movieRepo,
		jobRepo:   jobRepo,
		client:    client,
		scraper:   scraper,
		logger:    logger,
	}
}

type SyncRankingsParams struct {
	Period      string // daily, weekly, monthly, top250
	RankingType string // 0: all, 1: censored, 2: uncensored
	Year        string // for top250
	Limit       int    // max movies to process
}

// SyncRankings imports or updates movies appearing on JavDB ranking boards.
func (s *RankingsService) SyncRankings(ctx context.Context, params SyncRankingsParams) error {
	dedupeKey := fmt.Sprintf("rankings:%s:%s:%s", params.Period, params.RankingType, params.Year)
	paramsJSON, _ := json.Marshal(params)

	job, created, err := s.jobRepo.CreateOrGetJob(ctx, &models.Job{
		Kind:       "rankings",
		DedupeKey:  dedupeKey,
		ParamsJSON: string(paramsJSON),
	})
	if err != nil {
		return fmt.Errorf("create rankings job: %w", err)
	}
	if !created && (job.State == "running" || job.State == "queued") {
		return nil // Already running
	}

	workerID := "rankings-worker"
	claimed, err := s.jobRepo.ClaimJobByID(ctx, job.ID, workerID, 7200*time.Second)
	if err != nil || claimed == nil {
		return fmt.Errorf("claim rankings job: %w", err)
	}

	go s.executeRankings(context.Background(), claimed, params)
	return nil
}

func (s *RankingsService) executeRankings(ctx context.Context, job *models.Job, params SyncRankingsParams) {
	s.logger.Info("executing rankings sync", "period", params.Period, "type", params.RankingType)

	var movies []RankingMovieDTO
	var err error

	if params.Period == "top250" {
		movies, err = s.client.GetTop250Page(ctx, 1, params.RankingType, params.Year)
	} else {
		movies, err = s.client.GetRankings(ctx, params.Period, params.RankingType)
	}

	if err != nil {
		errMsg := err.Error()
		_ = s.jobRepo.FinishJob(ctx, job.ID, "failed", "{}", &errMsg)
		return
	}

	now := models.UTCNow()
	processedCount := 0
	scrapedCount := 0

	for _, rm := range movies {
		if params.Limit > 0 && processedCount >= params.Limit {
			break
		}

		// Filter excluded categories (VR, Western, Photo)
		if rm.VideoType == "western" || rm.VideoType == "vr" || rm.VideoType == "photo" {
			continue
		}

		code, _ := identity.NormalizeCode(rm.Number)
		if code == "" {
			continue
		}

		processedCount++

		// Check if movie already exists locally
		existing, err := s.movieRepo.GetMovie(ctx, code)
		if err == nil && existing != nil {
			// Check if resources are empty or last_scraped_at > 24 hours
			shouldRefresh := false
			if existing.LastScrapedAt == nil {
				shouldRefresh = true
			} else {
				lastTime, err := time.Parse(time.RFC3339, *existing.LastScrapedAt)
				if err == nil && time.Since(lastTime) > 24*time.Hour {
					shouldRefresh = true
				}
			}

			if shouldRefresh {
				_, _ = s.scraper.ScrapeMovie(ctx, code)
				scrapedCount++
			}
			continue
		}

		// Movie does not exist locally: insert placeholder and scrape
		_ = s.database.ExecWrite(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO offline_movies (
					code, title, category, first_seen_at, source_websites, actors, tags,
					is_enriched, scrape_policy, metadata_sources, manual_fields, legacy_metadata,
					created_at, updated_at
				) VALUES (?, ?, '未知', ?, '["javdb_ranking"]', '[]', '[]', 0, 'auto', '{}', '[]', '{}', ?, ?)
				ON CONFLICT(code) DO NOTHING;
			`, code, rm.Title, now, now, now)
			return err
		})

		_, _ = s.scraper.ScrapeMovie(ctx, code)
		scrapedCount++
	}

	resultMap := map[string]int{
		"total_fetched":   len(movies),
		"total_processed": processedCount,
		"total_scraped":   scrapedCount,
	}
	resBytes, _ := json.Marshal(resultMap)

	_ = s.jobRepo.FinishJob(ctx, job.ID, "succeeded", string(resBytes), nil)
	s.logger.Info("rankings sync completed", "job_id", job.ID, "stats", resultMap)
}
