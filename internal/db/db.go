package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB encapsulates single-write and multi-read SQLite connection pools.
type DB struct {
	writer *sql.DB
	reader *sql.DB
	path   string
}

// Open initializes the database connection pools for writer (1 conn) and reader (4 conns).
func Open(dbPath string) (*DB, error) {
	absPath, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	// modernc.org/sqlite DSN format
	// Configure foreign_keys=1, busy_timeout=5000, journal_mode=WAL, synchronous=NORMAL
	v := url.Values{}
	v.Add("_pragma", "foreign_keys(1)")
	v.Add("_pragma", "busy_timeout(5000)")
	v.Add("_pragma", "journal_mode(WAL)")
	v.Add("_pragma", "synchronous(NORMAL)")

	dsn := fmt.Sprintf("file:%s?%s", filepath.ToSlash(absPath), v.Encode())

	// 1. Open writer connection pool
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(0)

	// Verify writer pragmas and connectivity
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := writer.PingContext(ctx); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("ping writer db: %w", err)
	}

	if _, err := writer.ExecContext(ctx, `
		PRAGMA foreign_keys = ON;
		PRAGMA busy_timeout = 5000;
		PRAGMA journal_mode = WAL;
		PRAGMA synchronous = NORMAL;
	`); err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("set writer pragmas: %w", err)
	}

	// 2. Open reader connection pool
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("open sqlite reader: %w", err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)
	reader.SetConnMaxLifetime(0)

	if err := reader.PingContext(ctx); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return nil, fmt.Errorf("ping reader db: %w", err)
	}

	if _, err := reader.ExecContext(ctx, `
		PRAGMA foreign_keys = ON;
		PRAGMA busy_timeout = 5000;
	`); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		return nil, fmt.Errorf("set reader pragmas: %w", err)
	}

	return &DB{
		writer: writer,
		reader: reader,
		path:   absPath,
	}, nil
}

// Writer returns the single-writer *sql.DB pool.
func (d *DB) Writer() *sql.DB {
	return d.writer
}

// Reader returns the multi-reader *sql.DB pool.
func (d *DB) Reader() *sql.DB {
	return d.reader
}

// Path returns the absolute path to the database file.
func (d *DB) Path() string {
	return d.path
}

// Close closes both writer and reader connection pools.
func (d *DB) Close() error {
	var errW, errR error
	if d.writer != nil {
		errW = d.writer.Close()
	}
	if d.reader != nil {
		errR = d.reader.Close()
	}
	if errW != nil {
		return errW
	}
	return errR
}

// ExecWrite executes a function within an immediate write transaction.
func (d *DB) ExecWrite(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.writer.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelDefault,
		ReadOnly:  false,
	})
	if err != nil {
		return fmt.Errorf("begin write tx: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit write tx: %w", err)
	}
	return nil
}

// ExecRead executes a read-only query function using the reader pool.
func (d *DB) ExecRead(ctx context.Context, fn func(db *sql.DB) error) error {
	return fn(d.reader)
}
