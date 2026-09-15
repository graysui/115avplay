package javdb

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"mediavault/internal/db"
	"mediavault/internal/identity"
	"mediavault/internal/models"
	"sync"
	"time"
)

type SearchResult struct {
	Movies []models.Movie
	Cold   bool // True if an asynchronous background job was started
}

type SearchService struct {
	database  *db.DB
	movieRepo *db.MovieRepo
	jobRepo   *db.JobRepo
	scraper   *Scraper
	logger    *slog.Logger

	flightMu sync.Mutex
	inFlight map[string]chan struct{}
}

func NewSearchService(
	database *db.DB,
	movieRepo *db.MovieRepo,
	jobRepo *db.JobRepo,
	scraper *Scraper,
	logger *slog.Logger,
) *SearchService {
	if logger == nil {
		logger = slog.Default()
	}
	return &SearchService{
		database:  database,
		movieRepo: movieRepo,
		jobRepo:   jobRepo,
		scraper:   scraper,
		logger:    logger,
		inFlight:  make(map[string]chan struct{}),
	}
}

// Search executes local search or online code search with 6s budget.
func (s *SearchService) Search(ctx context.Context, keyword string, limit, offset int) (*SearchResult, error) {
	trimmed := keyword

	// 1. Try local search first
	localMatches, _, err := s.movieRepo.ListMovies(ctx, db.ListMoviesParams{
		SearchTerm: trimmed,
		Limit:      limit,
		Offset:     offset,
		SortBy:     "score",
		SortOrder:  "DESC",
	})
	if err == nil && len(localMatches) > 0 {
		return &SearchResult{Movies: localMatches}, nil
	}

	// 2. Check if keyword is a normalized code
	code, reason := identity.NormalizeCode(trimmed)
	if code == "" || reason == "unsupported_number_length" || reason == "ambiguous" {
		// Ordinary keyword (not a valid code): search local only, do not trigger network!
		return &SearchResult{Movies: []models.Movie{}}, nil
	}

	// 3. Check if movie with this exact code already exists in local DB
	existing, err := s.movieRepo.GetMovie(ctx, code)
	if err == nil && existing != nil {
		return &SearchResult{Movies: []models.Movie{*existing}}, nil
	}

	// 4. Online search with single-flight coalescing and 6-second budget
	ch, isLeader := s.getOrJoinFlight(code)
	if !isLeader {
		// Follower: wait for leader or context timeout
		select {
		case <-ch:
			// Completed, check if movie is in DB now
			m, _ := s.movieRepo.GetMovie(ctx, code)
			if m != nil {
				return &SearchResult{Movies: []models.Movie{*m}}, nil
			}
			return &SearchResult{Movies: []models.Movie{}}, nil
		case <-ctx.Done():
			return &SearchResult{Movies: []models.Movie{}, Cold: true}, nil
		}
	}

	// Leader: perform online search & scrape with 6s budget
	defer s.finishFlight(code, ch)

	// Create 6-second search context
	budgetCtx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	resultCh := make(chan *models.Movie, 1)
	errCh := make(chan error, 1)

	go func() {
		// 1. Insert placeholder movie row so scraper can run
		now := models.UTCNow()
		_ = s.database.ExecWrite(budgetCtx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(budgetCtx, `
				INSERT INTO offline_movies (
					code, title, category, first_seen_at, source_websites, actors, tags,
					is_enriched, scrape_policy, metadata_sources, manual_fields, legacy_metadata,
					created_at, updated_at
				) VALUES (?, ?, '未知', ?, '["online_search"]', '[]', '[]', 0, 'auto', '{}', '[]', '{}', ?, ?)
				ON CONFLICT(code) DO NOTHING;
			`, code, code, now, now, now)
			return err
		})

		scrapeRes, err := s.scraper.ScrapeMovie(budgetCtx, code)
		if err != nil {
			errCh <- err
			return
		}
		if scrapeRes.Status == "not_found" {
			resultCh <- nil
			return
		}

		m, err := s.movieRepo.GetMovie(budgetCtx, code)
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- m
	}()

	select {
	case m := <-resultCh:
		if m != nil {
			return &SearchResult{Movies: []models.Movie{*m}}, nil
		}
		return &SearchResult{Movies: []models.Movie{}}, nil

	case <-budgetCtx.Done():
		// Budget exceeded 6 seconds! Degrade to empty items, start background persistent search job
		s.logger.Info("search budget 6s exceeded, queueing background search job", "code", code)
		go s.spawnBackgroundSearchJob(code)
		return &SearchResult{Movies: []models.Movie{}, Cold: true}, nil

	case err := <-errCh:
		s.logger.Warn("online search scrape failed", "code", code, "error", err.Error())
		return &SearchResult{Movies: []models.Movie{}}, nil
	}
}

func (s *SearchService) getOrJoinFlight(code string) (chan struct{}, bool) {
	s.flightMu.Lock()
	defer s.flightMu.Unlock()

	if ch, exists := s.inFlight[code]; exists {
		return ch, false
	}
	ch := make(chan struct{})
	s.inFlight[code] = ch
	return ch, true
}

func (s *SearchService) finishFlight(code string, ch chan struct{}) {
	s.flightMu.Lock()
	delete(s.inFlight, code)
	close(ch)
	s.flightMu.Unlock()
}

func (s *SearchService) spawnBackgroundSearchJob(code string) {
	bgCtx := context.Background()
	_, _, _ = s.jobRepo.CreateOrGetJob(bgCtx, &models.Job{
		Kind:       "search",
		DedupeKey:  "search:" + code,
		ParamsJSON: fmt.Sprintf(`{"code":"%s"}`, code),
	})
	// Background execution: attempt once more
	scrapeCtx, cancel := context.WithTimeout(bgCtx, 30*time.Second)
	defer cancel()
	_, _ = s.scraper.ScrapeMovie(scrapeCtx, code)
}
