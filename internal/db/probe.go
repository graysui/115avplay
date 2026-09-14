package db

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// Capabilities holds detected SQLite features.
type Capabilities struct {
	Version            string
	Major              int
	Minor              int
	Patch              int
	HasJSON            bool
	HasPartialIndexes  bool
	ForeignKeysEnabled bool
	JournalMode        string
}

// ProbeCapabilities tests the database connection for required SQLite capabilities.
func ProbeCapabilities(ctx context.Context, database *sql.DB) (*Capabilities, error) {
	caps := &Capabilities{}

	// 1. Check sqlite_version()
	var version string
	if err := database.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&version); err != nil {
		return nil, fmt.Errorf("query sqlite_version: %w", err)
	}
	caps.Version = version

	parts := strings.Split(version, ".")
	if len(parts) >= 2 {
		caps.Major, _ = strconv.Atoi(parts[0])
		caps.Minor, _ = strconv.Atoi(parts[1])
		if len(parts) >= 3 {
			caps.Patch, _ = strconv.Atoi(parts[2])
		}
	}

	// modernc.org/sqlite requires at least SQLite 3.35 for RETURNING and generated columns
	if caps.Major < 3 || (caps.Major == 3 && caps.Minor < 35) {
		return nil, fmt.Errorf("sqlite version %s is too old; minimum required is 3.35.0", version)
	}

	// 2. Check JSON support
	var jsonValid int
	if err := database.QueryRowContext(ctx, "SELECT json_valid('{\"valid\":true}')").Scan(&jsonValid); err != nil || jsonValid != 1 {
		return nil, fmt.Errorf("sqlite json extension check failed: %w", err)
	}
	caps.HasJSON = true

	// 3. Check Foreign Keys
	var fk int
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return nil, fmt.Errorf("check pragma foreign_keys: %w", err)
	}
	caps.ForeignKeysEnabled = (fk == 1)

	// 4. Check Journal Mode
	var jm string
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&jm); err != nil {
		return nil, fmt.Errorf("check pragma journal_mode: %w", err)
	}
	caps.JournalMode = strings.ToUpper(jm)

	// 5. Check partial index capability
	// Attempt creating a temporary table with a partial index in memory or temp
	_, err := database.ExecContext(ctx, `
		CREATE TEMP TABLE IF NOT EXISTS _probe_partial_idx (id INTEGER PRIMARY KEY, status TEXT);
		CREATE INDEX IF NOT EXISTS _probe_idx ON _probe_partial_idx(id) WHERE status = 'active';
		DROP TABLE _probe_partial_idx;
	`)
	if err != nil {
		return nil, fmt.Errorf("partial index capability probe failed: %w", err)
	}
	caps.HasPartialIndexes = true

	return caps, nil
}
