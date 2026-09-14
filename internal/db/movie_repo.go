package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"mediavault/internal/models"
)

type MovieRepo struct {
	db *DB
}

func NewMovieRepo(db *DB) *MovieRepo {
	return &MovieRepo{db: db}
}

// ListMoviesParams contains parameters for movie listing and filtering.
type ListMoviesParams struct {
	Limit          int
	Offset         int
	SortBy         string // created, release, score, title, code
	SortOrder      string // ASC, DESC
	Category       string
	LibraryFilter  string // chinese_sub, censored, uncensored, 4k, fc2, domestic
	SearchTerm     string
	IncludeDeleted bool
}

type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanMovie(scanner rowScanner) (*models.Movie, error) {
	var m models.Movie
	var title, officialTitle, pubDate, relDate, previewImages sql.NullString
	var titleZh, descZh, coverURL, posterURL, maker, director sql.NullString
	var score sql.NullFloat64
	var runtimeTicks sql.NullInt64
	var policyReason, scrapeFailedReason, lastNotFoundDay, nextScrapeAt, lastScrapedAt, deletedAt sql.NullString

	err := scanner.Scan(
		&m.Code, &title, &officialTitle, &m.Category, &pubDate, &relDate, &m.FirstSeenAt,
		&previewImages, &m.SourceWebsites, &titleZh, &descZh, &coverURL, &posterURL,
		&m.Actors, &m.Tags, &maker, &director, &score, &runtimeTicks, &m.IsEnriched, &m.ScrapePolicy,
		&policyReason, &m.ScrapeStatus, &scrapeFailedReason, &m.NotFoundCount, &lastNotFoundDay,
		&m.PartialAttempts, &nextScrapeAt, &lastScrapedAt, &m.MetadataSources, &m.ManualFields,
		&m.LegacyMetadata, &deletedAt, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if title.Valid {
		m.Title = title.String
	}
	if officialTitle.Valid {
		m.OfficialTitle = officialTitle.String
	}
	if pubDate.Valid {
		m.PublishDate = &pubDate.String
	}
	if relDate.Valid {
		m.ReleaseDate = &relDate.String
	}
	if previewImages.Valid {
		m.PreviewImages = &previewImages.String
	}
	if titleZh.Valid {
		m.TitleZh = &titleZh.String
	}
	if descZh.Valid {
		m.DescriptionZh = &descZh.String
	}
	if coverURL.Valid {
		m.CoverURL = &coverURL.String
	}
	if posterURL.Valid {
		m.PosterURL = &posterURL.String
	}
	if maker.Valid {
		m.Maker = &maker.String
	}
	if director.Valid {
		m.Director = &director.String
	}
	if score.Valid {
		m.Score = &score.Float64
	}
	if runtimeTicks.Valid {
		m.RuntimeTicks = &runtimeTicks.Int64
	}
	if policyReason.Valid {
		m.PolicyReason = &policyReason.String
	}
	if scrapeFailedReason.Valid {
		m.ScrapeFailedReason = &scrapeFailedReason.String
	}
	if lastNotFoundDay.Valid {
		m.LastNotFoundDay = &lastNotFoundDay.String
	}
	if nextScrapeAt.Valid {
		m.NextScrapeAt = &nextScrapeAt.String
	}
	if lastScrapedAt.Valid {
		m.LastScrapedAt = &lastScrapedAt.String
	}
	if deletedAt.Valid {
		m.DeletedAt = &deletedAt.String
	}

	return &m, nil
}

// ListMovies retrieves a page of movies matching filters.
func (r *MovieRepo) ListMovies(ctx context.Context, p ListMoviesParams) ([]models.Movie, int, error) {
	if p.Limit <= 0 || p.Limit > 100 {
		p.Limit = 50
	}
	if p.Offset < 0 {
		p.Offset = 0
	}

	whereClauses := []string{"1=1"}
	var args []interface{}

	if !p.IncludeDeleted {
		whereClauses = append(whereClauses, "deleted_at IS NULL")
	}

	if p.Category != "" {
		whereClauses = append(whereClauses, "category = ?")
		args = append(args, p.Category)
	}

	if p.SearchTerm != "" {
		term := "%" + p.SearchTerm + "%"
		whereClauses = append(whereClauses, "(code LIKE ? OR title LIKE ? OR title_zh LIKE ? OR actors LIKE ?)")
		args = append(args, term, term, term, term)
	}

	// Apply library predicate filter
	switch p.LibraryFilter {
	case "chinese_sub":
		whereClauses = append(whereClauses, "EXISTS (SELECT 1 FROM offline_magnets m WHERE m.movie_code = offline_movies.code AND m.enabled = 1 AND m.has_chinese_sub = 1)")
	case "censored":
		whereClauses = append(whereClauses, "(category IN ('有码', '亚洲有码') OR EXISTS (SELECT 1 FROM offline_magnets m WHERE m.movie_code = offline_movies.code AND m.enabled = 1 AND m.is_censored = 1))")
	case "uncensored":
		whereClauses = append(whereClauses, "(category IN ('无码', '亚洲无码') OR EXISTS (SELECT 1 FROM offline_magnets m WHERE m.movie_code = offline_movies.code AND m.enabled = 1 AND m.is_censored = 0))")
	case "4k":
		whereClauses = append(whereClauses, "EXISTS (SELECT 1 FROM offline_magnets m WHERE m.movie_code = offline_movies.code AND m.enabled = 1 AND m.is_4k = 1)")
	case "fc2":
		whereClauses = append(whereClauses, "(category LIKE '%FC2%' OR code LIKE 'FC2%')")
	case "domestic":
		whereClauses = append(whereClauses, "(category LIKE '%国产%' OR category LIKE '%麻豆%')")
	}

	whereSQL := strings.Join(whereClauses, " AND ")

	// Total count query
	var total int
	countQuery := fmt.Sprintf("SELECT count(*) FROM offline_movies WHERE %s", whereSQL)
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		return database.QueryRowContext(ctx, countQuery, args...).Scan(&total)
	})
	if err != nil {
		return nil, 0, fmt.Errorf("count movies: %w", err)
	}

	// Order by
	orderDir := "DESC"
	if strings.ToUpper(p.SortOrder) == "ASC" {
		orderDir = "ASC"
	}

	var orderByCol string
	switch p.SortBy {
	case "release", "PremiereDate", "ProductionYear":
		orderByCol = "release_date"
	case "score", "CommunityRating":
		orderByCol = "score"
	case "title", "SortName":
		orderByCol = "title"
	case "code":
		orderByCol = "code"
	default:
		orderByCol = "created_at"
	}

	querySQL := fmt.Sprintf(`
		SELECT code, title, official_title, category, publish_date, release_date, first_seen_at,
		       preview_images, source_websites, title_zh, description_zh, cover_url, poster_url,
		       actors, tags, maker, director, score, runtime_ticks, is_enriched, scrape_policy,
		       policy_reason, scrape_status, scrape_failed_reason, not_found_count, last_not_found_day,
		       partial_attempts, next_scrape_at, last_scraped_at, metadata_sources, manual_fields,
		       legacy_metadata, deleted_at, created_at, updated_at
		FROM offline_movies
		WHERE %s
		ORDER BY %s %s, code ASC
		LIMIT ? OFFSET ?
	`, whereSQL, orderByCol, orderDir)

	queryArgs := append(args, p.Limit, p.Offset)

	var movies []models.Movie
	err = r.db.ExecRead(ctx, func(database *sql.DB) error {
		rows, err := database.QueryContext(ctx, querySQL, queryArgs...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			m, err := scanMovie(rows)
			if err != nil {
				return err
			}
			movies = append(movies, *m)
		}
		return nil
	})

	if err != nil {
		return nil, 0, fmt.Errorf("list movies: %w", err)
	}

	return movies, total, nil
}

// GetMovie retrieves a single movie by code.
func (r *MovieRepo) GetMovie(ctx context.Context, code string) (*models.Movie, error) {
	var m *models.Movie
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		row := database.QueryRowContext(ctx, `
			SELECT code, title, official_title, category, publish_date, release_date, first_seen_at,
			       preview_images, source_websites, title_zh, description_zh, cover_url, poster_url,
			       actors, tags, maker, director, score, runtime_ticks, is_enriched, scrape_policy,
			       policy_reason, scrape_status, scrape_failed_reason, not_found_count, last_not_found_day,
			       partial_attempts, next_scrape_at, last_scraped_at, metadata_sources, manual_fields,
			       legacy_metadata, deleted_at, created_at, updated_at
			FROM offline_movies
			WHERE code = ?
		`, code)
		var err error
		m, err = scanMovie(row)
		return err
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// UpsertMovie inserts or updates a movie record.
func (r *MovieRepo) UpsertMovie(ctx context.Context, m *models.Movie) error {
	now := models.UTCNow()
	if m.CreatedAt == "" {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	if m.Category == "" {
		m.Category = "未知"
	}
	if m.ScrapePolicy == "" {
		m.ScrapePolicy = "auto"
	}
	if m.ScrapeStatus == "" {
		m.ScrapeStatus = "idle"
	}
	if m.SourceWebsites == "" {
		m.SourceWebsites = "[]"
	}
	if m.Actors == "" {
		m.Actors = "[]"
	}
	if m.Tags == "" {
		m.Tags = "[]"
	}
	if m.MetadataSources == "" {
		m.MetadataSources = "{}"
	}
	if m.ManualFields == "" {
		m.ManualFields = "[]"
	}
	if m.LegacyMetadata == "" {
		m.LegacyMetadata = "{}"
	}

	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO offline_movies (
				code, title, official_title, category, publish_date, release_date, first_seen_at,
				preview_images, source_websites, title_zh, description_zh, cover_url, poster_url,
				actors, tags, maker, director, score, runtime_ticks, is_enriched, scrape_policy,
				policy_reason, scrape_status, scrape_failed_reason, not_found_count, last_not_found_day,
				partial_attempts, next_scrape_at, last_scraped_at, metadata_sources, manual_fields,
				legacy_metadata, deleted_at, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(code) DO UPDATE SET
				title = COALESCE(excluded.title, offline_movies.title),
				official_title = COALESCE(excluded.official_title, offline_movies.official_title),
				category = CASE WHEN excluded.category != '未知' THEN excluded.category ELSE offline_movies.category END,
				publish_date = COALESCE(excluded.publish_date, offline_movies.publish_date),
				release_date = COALESCE(excluded.release_date, offline_movies.release_date),
				title_zh = COALESCE(excluded.title_zh, offline_movies.title_zh),
				description_zh = COALESCE(excluded.description_zh, offline_movies.description_zh),
				cover_url = COALESCE(excluded.cover_url, offline_movies.cover_url),
				poster_url = COALESCE(excluded.poster_url, offline_movies.poster_url),
				actors = CASE WHEN excluded.actors != '[]' THEN excluded.actors ELSE offline_movies.actors END,
				tags = CASE WHEN excluded.tags != '[]' THEN excluded.tags ELSE offline_movies.tags END,
				maker = COALESCE(excluded.maker, offline_movies.maker),
				director = COALESCE(excluded.director, offline_movies.director),
				score = COALESCE(excluded.score, offline_movies.score),
				runtime_ticks = COALESCE(excluded.runtime_ticks, offline_movies.runtime_ticks),
				is_enriched = excluded.is_enriched,
				scrape_policy = excluded.scrape_policy,
				policy_reason = excluded.policy_reason,
				scrape_status = excluded.scrape_status,
				scrape_failed_reason = excluded.scrape_failed_reason,
				not_found_count = excluded.not_found_count,
				last_not_found_day = excluded.last_not_found_day,
				partial_attempts = excluded.partial_attempts,
				next_scrape_at = excluded.next_scrape_at,
				last_scraped_at = excluded.last_scraped_at,
				metadata_sources = excluded.metadata_sources,
				manual_fields = excluded.manual_fields,
				updated_at = excluded.updated_at
		`,
			m.Code, m.Title, m.OfficialTitle, m.Category, m.PublishDate, m.ReleaseDate, m.FirstSeenAt,
			m.PreviewImages, m.SourceWebsites, m.TitleZh, m.DescriptionZh, m.CoverURL, m.PosterURL,
			m.Actors, m.Tags, m.Maker, m.Director, m.Score, m.RuntimeTicks, m.IsEnriched, m.ScrapePolicy,
			m.PolicyReason, m.ScrapeStatus, m.ScrapeFailedReason, m.NotFoundCount, m.LastNotFoundDay,
			m.PartialAttempts, m.NextScrapeAt, m.LastScrapedAt, m.MetadataSources, m.ManualFields,
			m.LegacyMetadata, m.DeletedAt, m.CreatedAt, m.UpdatedAt,
		)
		return err
	})
}

// SoftDeleteMovie marks a movie deleted.
func (r *MovieRepo) SoftDeleteMovie(ctx context.Context, code string) error {
	now := models.UTCNow()
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE offline_movies SET deleted_at = ?, updated_at = ? WHERE code = ?`, now, now, code)
		return err
	})
}
