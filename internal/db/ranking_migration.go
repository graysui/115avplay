package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"mediavault/internal/models"
)

// EnsureRankingLibraries migrates existing databases to support the three
// ranking-based virtual libraries (周榜/月榜/TOP250) and the ranking_entries
// table. It is idempotent and safe to run on every startup.
func EnsureRankingLibraries(ctx context.Context, database *DB) (bool, error) {
	changed := false

	// 1. ranking_entries table.
	if err := database.ExecWrite(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			CREATE TABLE IF NOT EXISTS ranking_entries (
				board TEXT NOT NULL,
				rank INTEGER NOT NULL,
				code TEXT NOT NULL,
				title TEXT,
				updated_at TEXT NOT NULL,
				PRIMARY KEY (board, code)
			);
		`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_ranking_entries_board_rank ON ranking_entries(board, rank);`)
		return err
	}); err != nil {
		return false, fmt.Errorf("create ranking_entries: %w", err)
	}

	// 2. Extend the libraries.predicate CHECK constraint if needed by rebuilding
	//    the table (SQLite cannot alter a CHECK constraint in place).
	var ddl string
	if err := database.ExecRead(ctx, func(d *sql.DB) error {
		return d.QueryRowContext(ctx, `SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='table' AND name='libraries'`).Scan(&ddl)
	}); err != nil {
		return false, fmt.Errorf("read libraries ddl: %w", err)
	}

	if !strings.Contains(ddl, "ranking_weekly") {
		err := database.ExecWrite(ctx, func(tx *sql.Tx) error {
			stmts := []string{
				`DROP TABLE IF EXISTS libraries_old;`,
				`ALTER TABLE libraries RENAME TO libraries_old;`,
				`CREATE TABLE libraries (
					id TEXT PRIMARY KEY NOT NULL,
					name TEXT NOT NULL,
					predicate TEXT NOT NULL CHECK(predicate IN ('chinese_sub','censored','uncensored','4k','fc2','domestic','ranking_weekly','ranking_monthly','ranking_top250')),
					sort_order INTEGER NOT NULL,
					enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
					cover_url TEXT
				);`,
				`INSERT INTO libraries (id,name,predicate,sort_order,enabled,cover_url)
				 SELECT id,name,predicate,sort_order,enabled,cover_url FROM libraries_old;`,
				`DROP TABLE libraries_old;`,
			}
			for _, s := range stmts {
				if _, err := tx.ExecContext(ctx, s); err != nil {
					return fmt.Errorf("rebuild libraries: %w", err)
				}
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		changed = true
	}

	// 3. Insert the three ranking libraries on top and shift the existing ones down.
	if err := database.ExecWrite(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM libraries WHERE predicate LIKE 'ranking_%'`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE libraries SET sort_order = sort_order + 3 WHERE predicate NOT LIKE 'ranking_%';`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO libraries(id,name,predicate,sort_order) VALUES
			('lib_rank_weekly','周榜','ranking_weekly',1),
			('lib_rank_monthly','月榜','ranking_monthly',2),
			('lib_rank_top250','TOP250','ranking_top250',3);`); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return false, fmt.Errorf("seed ranking libraries: %w", err)
	} else {
		changed = true
	}

	return changed, nil
}

// UpsertRankingEntries replaces all entries of a board with the given list.
func (r *MovieRepo) UpsertRankingEntries(ctx context.Context, board string, codes []string, titles []string) error {
	now := models.UTCNow()
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM ranking_entries WHERE board = ?;`, board); err != nil {
			return err
		}
		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO ranking_entries (board, rank, code, title, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(board, code) DO UPDATE SET rank = excluded.rank, title = excluded.title, updated_at = excluded.updated_at;
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for i, code := range codes {
			title := ""
			if i < len(titles) {
				title = titles[i]
			}
			if _, err := stmt.ExecContext(ctx, board, i+1, code, title, now); err != nil {
				return err
			}
		}
		return nil
	})
}
