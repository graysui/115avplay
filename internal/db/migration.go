package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

//go:embed schema.sql
var TargetSchemaDDL string

const CurrentSchemaVersion = 1

var (
	ErrLegacyV0Database = errors.New("database has legacy-v0 schema; migration required")
	ErrNewerVersion     = errors.New("database schema version is newer than supported version; refuse to write")
)

type SchemaMetaInfo struct {
	Version  int
	ServerID string
	Checksum string
	MigratedAt string
}

// Checksum returns the SHA-256 hex string of the target DDL.
func SchemaChecksum() string {
	h := sha256.Sum256([]byte(TargetSchemaDDL))
	return hex.EncodeToString(h[:])
}

// DetectSchemaState checks the current database schema status.
func DetectSchemaState(ctx context.Context, database *sql.DB) (isInit bool, isLegacy bool, version int, err error) {
	// Check if schema_meta table exists
	var metaTableCount int
	err = database.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_meta'").Scan(&metaTableCount)
	if err != nil {
		return false, false, 0, fmt.Errorf("check schema_meta table: %w", err)
	}

	if metaTableCount > 0 {
		var verStr string
		err = database.QueryRowContext(ctx, "SELECT value FROM schema_meta WHERE key='schema_version'").Scan(&verStr)
		if err != nil {
			return false, false, 0, fmt.Errorf("read schema_version: %w", err)
		}
		ver, err := strconv.Atoi(verStr)
		if err != nil {
			return false, false, 0, fmt.Errorf("parse schema_version %s: %w", verStr, err)
		}
		return true, false, ver, nil
	}

	// Check if legacy business tables exist
	var legacyTableCount int
	err = database.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('offline_movies', 'offline_magnets')").Scan(&legacyTableCount)
	if err != nil {
		return false, false, 0, fmt.Errorf("check legacy tables: %w", err)
	}

	if legacyTableCount > 0 {
		return false, true, 0, nil
	}

	// Completely empty database
	return false, false, 0, nil
}

// InitEmptyDatabase applies TargetSchemaDDL and sets schema_meta if the DB is empty.
func InitEmptyDatabase(ctx context.Context, d *DB) (*SchemaMetaInfo, error) {
	isInit, isLegacy, version, err := DetectSchemaState(ctx, d.writer)
	if err != nil {
		return nil, err
	}

	if isLegacy {
		return nil, ErrLegacyV0Database
	}

	if isInit {
		if version > CurrentSchemaVersion {
			return nil, fmt.Errorf("%w: version=%d > %d", ErrNewerVersion, version, CurrentSchemaVersion)
		}
		return ReadSchemaMeta(ctx, d.reader)
	}

	// Empty DB: apply target DDL in a single transaction
	serverID := uuid.New().String()
	migratedAt := time.Now().UTC().Format(time.RFC3339)
	checksum := SchemaChecksum()

	err = d.ExecWrite(ctx, func(tx *sql.Tx) error {
		// Execute DDL statements
		statements := splitSQLStatements(TargetSchemaDDL)
		for _, stmt := range statements {
			trimmed := strings.TrimSpace(stmt)
			if trimmed == "" {
				continue
			}
			if _, err := tx.ExecContext(ctx, trimmed); err != nil {
				return fmt.Errorf("execute schema statement [%s...]: %w", truncate(trimmed, 40), err)
			}
		}

		// Insert metadata
		insertMeta := `INSERT OR REPLACE INTO schema_meta (key, value) VALUES (?, ?)`
		metas := [][2]string{
			{"schema_version", strconv.Itoa(CurrentSchemaVersion)},
			{"server_id", serverID},
			{"migration_at", migratedAt},
			{"migration_checksum", checksum},
		}
		for _, m := range metas {
			if _, err := tx.ExecContext(ctx, insertMeta, m[0], m[1]); err != nil {
				return fmt.Errorf("insert meta %s: %w", m[0], err)
			}
		}

		// Run foreign_key_check and quick_check
		var fkErrCount int
		rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return fmt.Errorf("foreign_key_check: %w", err)
		}
		for rows.Next() {
			fkErrCount++
		}
		_ = rows.Close()
		if fkErrCount > 0 {
			return fmt.Errorf("foreign_key_check failed with %d errors", fkErrCount)
		}

		var qc string
		if err := tx.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&qc); err != nil || qc != "ok" {
			return fmt.Errorf("quick_check failed: %s (err: %v)", qc, err)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("init empty database: %w", err)
	}

	return &SchemaMetaInfo{
		Version:    CurrentSchemaVersion,
		ServerID:   serverID,
		Checksum:   checksum,
		MigratedAt: migratedAt,
	}, nil
}

// ReadSchemaMeta reads schema metadata from schema_meta table.
func ReadSchemaMeta(ctx context.Context, database *sql.DB) (*SchemaMetaInfo, error) {
	rows, err := database.QueryContext(ctx, "SELECT key, value FROM schema_meta")
	if err != nil {
		return nil, fmt.Errorf("query schema_meta: %w", err)
	}
	defer rows.Close()

	info := &SchemaMetaInfo{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		switch k {
		case "schema_version":
			info.Version, _ = strconv.Atoi(v)
		case "server_id":
			info.ServerID = v
		case "migration_checksum":
			info.Checksum = v
		case "migration_at":
			info.MigratedAt = v
		}
	}
	return info, nil
}

func splitSQLStatements(sqlText string) []string {
	var statements []string
	var current strings.Builder
	lines := strings.Split(sqlText, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		current.WriteString(line)
		current.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			statements = append(statements, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 && strings.TrimSpace(current.String()) != "" {
		statements = append(statements, current.String())
	}
	return statements
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
