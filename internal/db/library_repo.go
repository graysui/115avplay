package db

import (
	"context"
	"database/sql"
	"fmt"

	"mediavault/internal/models"
)

type LibraryRepo struct {
	db *DB
}

func NewLibraryRepo(db *DB) *LibraryRepo {
	return &LibraryRepo{db: db}
}

// ListLibraries returns the 6 fixed libraries ordered by sort_order.
func (r *LibraryRepo) ListLibraries(ctx context.Context) ([]models.Library, error) {
	var libs []models.Library
	err := r.db.ExecRead(ctx, func(database *sql.DB) error {
		rows, err := database.QueryContext(ctx, `
			SELECT id, name, predicate, sort_order, enabled, cover_url
			FROM libraries
			ORDER BY sort_order ASC
		`)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var l models.Library
			if err := rows.Scan(&l.ID, &l.Name, &l.Predicate, &l.SortOrder, &l.Enabled, &l.CoverURL); err != nil {
				return err
			}
			libs = append(libs, l)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list libraries: %w", err)
	}
	return libs, nil
}

// UpdateLibrary updates name, sort_order, enabled, or cover_url of a fixed library.
func (r *LibraryRepo) UpdateLibrary(ctx context.Context, l *models.Library) error {
	return r.db.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE libraries
			SET name = ?, sort_order = ?, enabled = ?, cover_url = ?
			WHERE id = ?
		`, l.Name, l.SortOrder, l.Enabled, l.CoverURL, l.ID)
		return err
	})
}
