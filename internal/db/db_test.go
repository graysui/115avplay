package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestDBOpenAndProbe(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	caps, err := ProbeCapabilities(ctx, database.writer)
	if err != nil {
		t.Fatalf("ProbeCapabilities failed: %v", err)
	}

	if !caps.HasJSON {
		t.Errorf("expected HasJSON to be true")
	}
	if !caps.HasPartialIndexes {
		t.Errorf("expected HasPartialIndexes to be true")
	}
	if !caps.ForeignKeysEnabled {
		t.Errorf("expected ForeignKeysEnabled to be true")
	}
	if caps.JournalMode != "WAL" {
		t.Errorf("expected JournalMode to be WAL, got %s", caps.JournalMode)
	}
}

func TestInitEmptyDatabase(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "empty.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// 1. First run on empty DB: should initialize schema
	meta, err := InitEmptyDatabase(ctx, database)
	if err != nil {
		t.Fatalf("InitEmptyDatabase failed: %v", err)
	}
	if meta.Version != 1 {
		t.Errorf("expected version 1, got %d", meta.Version)
	}
	if meta.ServerID == "" {
		t.Errorf("expected non-empty ServerID")
	}

	// 2. Second run on already initialized DB: should recognize and return existing meta
	meta2, err := InitEmptyDatabase(ctx, database)
	if err != nil {
		t.Fatalf("second InitEmptyDatabase failed: %v", err)
	}
	if meta2.ServerID != meta.ServerID {
		t.Errorf("expected stable ServerID across inits: %s vs %s", meta.ServerID, meta2.ServerID)
	}

	// 3. Verify libraries table has 6 default libraries
	var libCount int
	err = database.reader.QueryRowContext(ctx, "SELECT count(*) FROM libraries").Scan(&libCount)
	if err != nil {
		t.Fatalf("query libraries failed: %v", err)
	}
	if libCount != 6 {
		t.Errorf("expected 6 default libraries, got %d", libCount)
	}
}

func TestExclusiveFileLock(t *testing.T) {
	tempDir := t.TempDir()
	lockPath := filepath.Join(tempDir, ".test.lock")

	lock1, err := AcquireExclusiveLock(lockPath)
	if err != nil {
		t.Fatalf("acquire lock 1 failed: %v", err)
	}

	if !IsLocked(lockPath) {
		t.Errorf("expected file to be locked")
	}

	// Release lock
	if err := lock1.Release(); err != nil {
		t.Fatalf("release lock failed: %v", err)
	}

	if IsLocked(lockPath) {
		t.Errorf("expected file to be unlocked")
	}
}
