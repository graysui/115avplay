package db

import (
	"context"
	"database/sql"
	"fmt"

	"mediavault/internal/models"
)

type ProgressRepo struct {
	db *DB
}

func NewProgressRepo(db *DB) *ProgressRepo {
	return &ProgressRepo{db: db}
}

// CreatePlaySession starts a new playback session.
func (r *ProgressRepo) CreatePlaySession(ctx context.Context, s *models.PlaySession) error {
	now := models.UTCNow()
	if s.StartedAt == nil {
		s.StartedAt = &now
	}
	s.UpdatedAt = now

	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO play_sessions (
				id, user_id, movie_code, resource_key, asset_id, device_id,
				state, position_ticks, duration_ticks, last_sequence, is_paused,
				counted_complete, lease_until, started_at, closed_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			s.ID, s.UserID, s.MovieCode, s.ResourceKey, s.AssetID, s.DeviceID,
			s.State, s.PositionTicks, s.DurationTicks, s.LastSequence, s.IsPaused,
			s.CountedComplete, s.LeaseUntil, s.StartedAt, s.ClosedAt, s.UpdatedAt,
		)
		return err
	})
}

// UpdatePlayProgress updates session progress and user_progress.
func (r *ProgressRepo) UpdatePlayProgress(ctx context.Context, sessionID string, positionTicks int64, durationTicks *int64, isPaused bool) error {
	now := models.UTCNow()
	pausedInt := 0
	if isPaused {
		pausedInt = 1
	}

	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		var userID, movieCode string
		var curComplete int
		row := tx.QueryRowContext(ctx, `
			SELECT user_id, movie_code, counted_complete
			FROM play_sessions
			WHERE id = ? AND state != 'stopped'
		`, sessionID)
		if err := row.Scan(&userID, &movieCode, &curComplete); err != nil {
			return err
		}

		// Check >= 90% completed condition
		shouldMarkComplete := false
		if durationTicks != nil && *durationTicks > 0 {
			if positionTicks*10 >= (*durationTicks)*9 {
				shouldMarkComplete = true
			}
		}

		newComplete := curComplete
		playCountIncrement := 0
		if shouldMarkComplete && curComplete == 0 {
			newComplete = 1
			playCountIncrement = 1
		}

		// Update play_sessions
		if _, err := tx.ExecContext(ctx, `
			UPDATE play_sessions
			SET position_ticks = ?, duration_ticks = COALESCE(?, duration_ticks),
			    is_paused = ?, counted_complete = ?, updated_at = ?
			WHERE id = ?
		`, positionTicks, durationTicks, pausedInt, newComplete, now, sessionID); err != nil {
			return err
		}

		// Upsert user_progress
		playedVal := 0
		if shouldMarkComplete {
			playedVal = 1
		}

		_, err := tx.ExecContext(ctx, `
			INSERT INTO user_progress (
				user_id, movie_code, active_session_id, position_ticks, duration_ticks,
				played, play_count, last_played_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, movie_code) DO UPDATE SET
				active_session_id = excluded.active_session_id,
				position_ticks = excluded.position_ticks,
				duration_ticks = COALESCE(excluded.duration_ticks, user_progress.duration_ticks),
				played = CASE WHEN excluded.played = 1 THEN 1 ELSE user_progress.played END,
				play_count = user_progress.play_count + excluded.play_count,
				last_played_at = excluded.last_played_at,
				updated_at = excluded.updated_at
		`, userID, movieCode, sessionID, positionTicks, durationTicks, playedVal, playCountIncrement, now, now)

		return err
	})
}

// ClosePlaySession marks session stopped and clears active_session_id in user_progress.
func (r *ProgressRepo) ClosePlaySession(ctx context.Context, sessionID string) error {
	now := models.UTCNow()
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		var userID, movieCode string
		row := tx.QueryRowContext(ctx, `SELECT user_id, movie_code FROM play_sessions WHERE id = ?`, sessionID)
		if err := row.Scan(&userID, &movieCode); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return err
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE play_sessions SET state = 'stopped', closed_at = ?, updated_at = ? WHERE id = ?
		`, now, now, sessionID); err != nil {
			return err
		}

		_, err := tx.ExecContext(ctx, `
			UPDATE user_progress SET active_session_id = NULL, updated_at = ?
			WHERE user_id = ? AND movie_code = ? AND active_session_id = ?
		`, now, userID, movieCode, sessionID)
		return err
	})
}

// ResumeItem contains a movie and its playback progress.
type ResumeItem struct {
	Movie    models.Movie
	Progress models.UserProgress
}

// ListResumeMovies returns in-progress movies (played=0 AND position_ticks>0) ordered by last_played_at DESC.
func (r *ProgressRepo) ListResumeMovies(ctx context.Context, userID string, limit int) ([]ResumeItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	query := `
		SELECT m.code, m.title, m.official_title, m.category, m.publish_date, m.release_date, m.first_seen_at,
		       m.preview_images, m.source_websites, m.title_zh, m.description_zh, m.cover_url, m.poster_url,
		       m.actors, m.tags, m.maker, m.director, m.score, m.runtime_ticks, m.is_enriched, m.scrape_policy,
		       m.policy_reason, m.scrape_status, m.scrape_failed_reason, m.not_found_count, m.last_not_found_day,
		       m.partial_attempts, m.next_scrape_at, m.last_scraped_at, m.metadata_sources, m.manual_fields,
		       m.legacy_metadata, m.deleted_at, m.created_at, m.updated_at,
		       p.user_id, p.movie_code, p.active_session_id, p.position_ticks, p.duration_ticks,
		       p.played, p.favorite, p.play_count, p.last_played_at, p.updated_at
		FROM user_progress p
		JOIN offline_movies m ON m.code = p.movie_code
		WHERE p.user_id = ? AND p.played = 0 AND p.position_ticks > 0 AND m.deleted_at IS NULL
		ORDER BY p.last_played_at DESC
		LIMIT ?
	`

	var items []ResumeItem
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		rows, err := database.QueryContext(ctx, query, userID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var m models.Movie
			var p models.UserProgress
			err := rows.Scan(
				&m.Code, &m.Title, &m.OfficialTitle, &m.Category, &m.PublishDate, &m.ReleaseDate, &m.FirstSeenAt,
				&m.PreviewImages, &m.SourceWebsites, &m.TitleZh, &m.DescriptionZh, &m.CoverURL, &m.PosterURL,
				&m.Actors, &m.Tags, &m.Maker, &m.Director, &m.Score, &m.RuntimeTicks, &m.IsEnriched, &m.ScrapePolicy,
				&m.PolicyReason, &m.ScrapeStatus, &m.ScrapeFailedReason, &m.NotFoundCount, &m.LastNotFoundDay,
				&m.PartialAttempts, &m.NextScrapeAt, &m.LastScrapedAt, &m.MetadataSources, &m.ManualFields,
				&m.LegacyMetadata, &m.DeletedAt, &m.CreatedAt, &m.UpdatedAt,
				&p.UserID, &p.MovieCode, &p.ActiveSessionID, &p.PositionTicks, &p.DurationTicks,
				&p.Played, &p.Favorite, &p.PlayCount, &p.LastPlayedAt, &p.UpdatedAt,
			)
			if err != nil {
				return err
			}
			items = append(items, ResumeItem{Movie: m, Progress: p})
		}
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("list resume movies: %w", err)
	}
	return items, nil
}
