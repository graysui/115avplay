package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"mediavault/internal/config"
	"mediavault/internal/models"
)

var ErrRevisionMismatch = errors.New("configuration revision mismatch (concurrent modification)")

// SettingsRepo manages system_settings, schedules, and alerts.
type SettingsRepo struct {
	database *DB
}

func NewSettingsRepo(database *DB) *SettingsRepo {
	return &SettingsRepo{database: database}
}

// GetAllSettings returns all rows from system_settings and the current maximum revision.
func (r *SettingsRepo) GetAllSettings(ctx context.Context) (map[string]models.SystemSetting, int, error) {
	settings := make(map[string]models.SystemSetting)
	maxRev := 0

	err := r.database.ExecRead(ctx, func(d *sql.DB) error {
		rows, err := d.QueryContext(ctx, `SELECT key, value, is_secret, revision, updated_at FROM system_settings;`)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var s models.SystemSetting
			if err := rows.Scan(&s.Key, &s.Value, &s.IsSecret, &s.Revision, &s.UpdatedAt); err != nil {
				return err
			}
			settings[s.Key] = s
			if s.Revision > maxRev {
				maxRev = s.Revision
			}
		}
		return rows.Err()
	})

	if err != nil {
		return nil, 0, err
	}
	if maxRev == 0 {
		maxRev = 1
	}
	return settings, maxRev, nil
}

// GetSetting returns a single system setting.
func (r *SettingsRepo) GetSetting(ctx context.Context, key string) (*models.SystemSetting, error) {
	var s models.SystemSetting
	err := r.database.ExecRead(ctx, func(d *sql.DB) error {
		row := d.QueryRowContext(ctx, `SELECT key, value, is_secret, revision, updated_at FROM system_settings WHERE key = ?;`, key)
		return row.Scan(&s.Key, &s.Value, &s.IsSecret, &s.Revision, &s.UpdatedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// GetRevision returns the current max revision in system_settings, or 1 if empty.
func (r *SettingsRepo) GetRevision(ctx context.Context) (int, error) {
	var maxRev sql.NullInt64
	err := r.database.ExecRead(ctx, func(d *sql.DB) error {
		row := d.QueryRowContext(ctx, `SELECT MAX(revision) FROM system_settings;`)
		return row.Scan(&maxRev)
	})
	if err != nil {
		return 0, err
	}
	if !maxRev.Valid || maxRev.Int64 == 0 {
		return 1, nil
	}
	return int(maxRev.Int64), nil
}

// UpdateSettings updates settings atomically with optimistic locking check.
func (r *SettingsRepo) UpdateSettings(ctx context.Context, expectedRev int, normalValues map[string]string, secretEnvelopes map[string]string, secretsToClear []string) (int, error) {
	var newRev int
	err := r.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		var currentMax sql.NullInt64
		row := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM system_settings;`)
		if err := row.Scan(&currentMax); err != nil {
			return err
		}

		currRev := 1
		if currentMax.Valid && currentMax.Int64 > 0 {
			currRev = int(currentMax.Int64)
		}

		if expectedRev > 0 && currRev != expectedRev {
			return fmt.Errorf("%w: expected %d, got %d", ErrRevisionMismatch, expectedRev, currRev)
		}

		newRev = currRev + 1
		now := models.UTCNow()

		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO system_settings (key, value, is_secret, revision, updated_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				value = excluded.value,
				is_secret = excluded.is_secret,
				revision = excluded.revision,
				updated_at = excluded.updated_at;
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for k, v := range normalValues {
			if _, err := stmt.ExecContext(ctx, k, v, 0, newRev, now); err != nil {
				return err
			}
		}

		for k, envJSON := range secretEnvelopes {
			if _, err := stmt.ExecContext(ctx, k, envJSON, 1, newRev, now); err != nil {
				return err
			}
		}

		for _, k := range secretsToClear {
			if _, err := tx.ExecContext(ctx, `DELETE FROM system_settings WHERE key = ?;`, k); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return 0, err
	}
	return newRev, nil
}

// GetDecryptedSecret retrieves and decrypts a secret value from system_settings.
func (r *SettingsRepo) GetDecryptedSecret(ctx context.Context, mk *config.MasterKey, key string) (string, error) {
	s, err := r.GetSetting(ctx, key)
	if err != nil {
		return "", err
	}
	if s == nil || s.Value == nil || *s.Value == "" {
		return "", nil
	}
	if s.IsSecret != 1 {
		return *s.Value, nil
	}
	if mk == nil {
		return "", errors.New("master key not initialized")
	}

	plain, err := mk.Decrypt(key, *s.Value)
	if err != nil {
		return "", fmt.Errorf("decrypt secret %s: %w", key, err)
	}
	return string(plain), nil
}

// --- Schedules Repo ---

func (r *SettingsRepo) ListSchedules(ctx context.Context) ([]models.Schedule, error) {
	var list []models.Schedule
	err := r.database.ExecRead(ctx, func(d *sql.DB) error {
		rows, err := d.QueryContext(ctx, `SELECT id, kind, timezone, rule_json, params_json, enabled, last_slot, next_run_at, updated_at FROM schedules ORDER BY id;`)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var s models.Schedule
			if err := rows.Scan(&s.ID, &s.Kind, &s.Timezone, &s.RuleJSON, &s.ParamsJSON, &s.Enabled, &s.LastSlot, &s.NextRunAt, &s.UpdatedAt); err != nil {
				return err
			}
			list = append(list, s)
		}
		return rows.Err()
	})
	return list, err
}

func (r *SettingsRepo) GetSchedule(ctx context.Context, id string) (*models.Schedule, error) {
	var s models.Schedule
	err := r.database.ExecRead(ctx, func(d *sql.DB) error {
		row := d.QueryRowContext(ctx, `SELECT id, kind, timezone, rule_json, params_json, enabled, last_slot, next_run_at, updated_at FROM schedules WHERE id = ?;`, id)
		return row.Scan(&s.ID, &s.Kind, &s.Timezone, &s.RuleJSON, &s.ParamsJSON, &s.Enabled, &s.LastSlot, &s.NextRunAt, &s.UpdatedAt)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SettingsRepo) UpsertSchedule(ctx context.Context, s *models.Schedule) error {
	now := models.UTCNow()
	s.UpdatedAt = now
	return r.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO schedules (id, kind, timezone, rule_json, params_json, enabled, last_slot, next_run_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				kind = excluded.kind,
				timezone = excluded.timezone,
				rule_json = excluded.rule_json,
				params_json = excluded.params_json,
				enabled = excluded.enabled,
				last_slot = excluded.last_slot,
				next_run_at = excluded.next_run_at,
				updated_at = excluded.updated_at;
		`, s.ID, s.Kind, s.Timezone, s.RuleJSON, s.ParamsJSON, s.Enabled, s.LastSlot, s.NextRunAt, s.UpdatedAt)
		return err
	})
}

func (r *SettingsRepo) DeleteSchedule(ctx context.Context, id string) error {
	return r.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?;`, id)
		return err
	})
}

// --- Alerts Repo ---

func (r *SettingsRepo) ListAlerts(ctx context.Context, activeOnly bool) ([]models.Alert, error) {
	var list []models.Alert
	query := `SELECT key, state, message, first_seen_at, last_seen_at, last_delivered_at, next_delivery_at, delivery_attempts, delivery_error FROM alerts`
	if activeOnly {
		query += ` WHERE state = 'active'`
	}
	query += ` ORDER BY last_seen_at DESC;`

	err := r.database.ExecRead(ctx, func(d *sql.DB) error {
		rows, err := d.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var a models.Alert
			if err := rows.Scan(&a.Key, &a.State, &a.Message, &a.FirstSeenAt, &a.LastSeenAt, &a.LastDeliveredAt, &a.NextDeliveryAt, &a.DeliveryAttempts, &a.DeliveryError); err != nil {
				return err
			}
			list = append(list, a)
		}
		return rows.Err()
	})
	return list, err
}

func (r *SettingsRepo) UpsertAlert(ctx context.Context, a *models.Alert) error {
	now := models.UTCNow()
	if a.FirstSeenAt == "" {
		a.FirstSeenAt = now
	}
	a.LastSeenAt = now
	return r.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO alerts (key, state, message, first_seen_at, last_seen_at, last_delivered_at, next_delivery_at, delivery_attempts, delivery_error)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(key) DO UPDATE SET
				state = excluded.state,
				message = excluded.message,
				last_seen_at = excluded.last_seen_at,
				last_delivered_at = excluded.last_delivered_at,
				next_delivery_at = excluded.next_delivery_at,
				delivery_attempts = excluded.delivery_attempts,
				delivery_error = excluded.delivery_error;
		`, a.Key, a.State, a.Message, a.FirstSeenAt, a.LastSeenAt, a.LastDeliveredAt, a.NextDeliveryAt, a.DeliveryAttempts, a.DeliveryError)
		return err
	})
}

func (r *SettingsRepo) ResolveAlert(ctx context.Context, key string) error {
	now := models.UTCNow()
	return r.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE alerts SET state = 'resolved', last_seen_at = ? WHERE key = ?;`, now, key)
		return err
	})
}
