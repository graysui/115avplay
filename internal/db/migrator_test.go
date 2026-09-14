package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func createLegacySampleDB(t *testing.T, dbPath string) {
	database, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("create legacy db failed: %v", err)
	}
	defer database.Close()

	// Legacy-v0 schema without schema_meta
	schema := `
		CREATE TABLE offline_movies (
			code TEXT PRIMARY KEY,
			title TEXT,
			category TEXT,
			publish_date TEXT,
			preview_images TEXT,
			source_websites TEXT,
			title_zh TEXT,
			description_zh TEXT,
			cover_url TEXT,
			poster_url TEXT,
			actors TEXT,
			tags TEXT,
			maker TEXT,
			director TEXT,
			score REAL,
			is_enriched INTEGER,
			scrape_status INTEGER,
			scrape_failed_reason TEXT,
			scrape_retry_count INTEGER,
			last_scraped_at DATETIME
		);

		CREATE TABLE offline_magnets (
			info_hash TEXT PRIMARY KEY,
			movie_code TEXT NOT NULL,
			magnet_url TEXT NOT NULL,
			title TEXT,
			size_mb INTEGER,
			section TEXT,
			quality_label TEXT,
			website TEXT,
			publish_date TEXT,
			priority_score INTEGER,
			is_preferred BOOLEAN,
			source_type TEXT,
			storage_cid TEXT,
			target_file_id TEXT,
			target_pick_code TEXT,
			target_file_name TEXT,
			target_file_size INTEGER,
			target_container TEXT,
			transfer_status INTEGER,
			is_available BOOLEAN,
			temp_expire_at DATETIME,
			last_error TEXT
		);
	`
	if _, err := database.Exec(schema); err != nil {
		t.Fatalf("create legacy schema failed: %v", err)
	}

	// Insert legacy sample data
	// 1. Movie with complete metadata (old is_enriched=1, all fields present)
	_, err = database.Exec(`
		INSERT INTO offline_movies (code, title, category, publish_date, source_websites, title_zh, description_zh, cover_url, actors, tags, score, is_enriched)
		VALUES ('IPX-123', 'Sample Title', '有码', '2024-01-01', 'siteA,siteB,siteA', '中文标题', '中文详细简介', 'https://example.com/cover.jpg', '["Actor A"]', '["Tag 1"]', 4.5, 1);
	`)
	if err != nil {
		t.Fatalf("insert legacy movie 1 failed: %v", err)
	}

	// 2. Movie with missing description (old is_enriched=1, but missing description_zh -> should become is_enriched=3, auto)
	_, err = database.Exec(`
		INSERT INTO offline_movies (code, title, category, publish_date, title_zh, is_enriched)
		VALUES ('SSNI-999', 'Sample 2', '无码', '2024-02-01', '中文标题2', 1);
	`)
	if err != nil {
		t.Fatalf("insert legacy movie 2 failed: %v", err)
	}

	// 3. FC2 movie (old is_enriched=2 -> should become exempt/category)
	_, err = database.Exec(`
		INSERT INTO offline_movies (code, title, category, is_enriched)
		VALUES ('FC2-PPV-1234567', 'FC2 Sample', 'FC2', 2);
	`)
	if err != nil {
		t.Fatalf("insert legacy movie 3 failed: %v", err)
	}

	// Magnets
	_, err = database.Exec(`
		INSERT INTO offline_magnets (info_hash, movie_code, magnet_url, title, size_mb, is_available)
		VALUES ('HASH111111111111111111111111111111111111', 'IPX-123', 'magnet:?xt=urn:btih:HASH111111111111111111111111111111111111', 'IPX-123 中文字幕 4K 有码', 2048, 1);
	`)
	if err != nil {
		t.Fatalf("insert legacy magnet 1 failed: %v", err)
	}

	_, err = database.Exec(`
		INSERT INTO offline_magnets (info_hash, movie_code, magnet_url, title, size_mb)
		VALUES ('HASH222222222222222222222222222222222222', 'SSNI-999', 'magnet:?xt=urn:btih:HASH222222222222222222222222222222222222', 'SSNI-999 720P', 1024);
	`)
	if err != nil {
		t.Fatalf("insert legacy magnet 2 failed: %v", err)
	}
}

func TestLegacyV0Migration(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "legacy.db")

	createLegacySampleDB(t, dbPath)

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer database.Close()

	ctx := context.Background()

	// 1. Verify schema state detection
	isInit, isLegacy, ver, err := DetectSchemaState(ctx, database.writer)
	if err != nil {
		t.Fatalf("DetectSchemaState failed: %v", err)
	}
	if isInit || !isLegacy || ver != 0 {
		t.Fatalf("expected isLegacy=true, got isInit=%v, isLegacy=%v, ver=%d", isInit, isLegacy, ver)
	}

	// 2. Perform migration
	migrator := NewMigrator(database)
	meta, err := migrator.MigrateLegacyV0(ctx)
	if err != nil {
		t.Fatalf("MigrateLegacyV0 failed: %v", err)
	}
	if meta.Version != 1 {
		t.Errorf("expected migrated version 1, got %d", meta.Version)
	}
	if meta.ServerID == "" {
		t.Errorf("expected non-empty ServerID")
	}

	// 3. Verify Movie 1: complete -> is_enriched=1
	movieRepo := NewMovieRepo(database)
	m1, err := movieRepo.GetMovie(ctx, "IPX-123")
	if err != nil {
		t.Fatalf("GetMovie IPX-123 failed: %v", err)
	}
	if m1.IsEnriched != 1 {
		t.Errorf("expected IPX-123 is_enriched=1, got %d", m1.IsEnriched)
	}
	if m1.SourceWebsites != `["siteA","siteB"]` {
		t.Errorf("expected deduplicated source websites JSON, got %s", m1.SourceWebsites)
	}

	// 4. Verify Movie 2: missing description -> converted to is_enriched=3, auto
	m2, err := movieRepo.GetMovie(ctx, "SSNI-999")
	if err != nil {
		t.Fatalf("GetMovie SSNI-999 failed: %v", err)
	}
	if m2.IsEnriched != 3 {
		t.Errorf("expected SSNI-999 is_enriched=3, got %d", m2.IsEnriched)
	}
	if m2.ScrapePolicy != "auto" {
		t.Errorf("expected SSNI-999 scrape_policy=auto, got %s", m2.ScrapePolicy)
	}

	// 5. Verify Movie 3: FC2 -> exempt/category
	m3, err := movieRepo.GetMovie(ctx, "FC2-PPV-1234567")
	if err != nil {
		t.Fatalf("GetMovie FC2-PPV-1234567 failed: %v", err)
	}
	if m3.ScrapePolicy != "exempt" {
		t.Errorf("expected FC2 scrape_policy=exempt, got %s", m3.ScrapePolicy)
	}
	if m3.PolicyReason == nil || *m3.PolicyReason != "category" {
		t.Errorf("expected FC2 policy_reason=category, got %v", m3.PolicyReason)
	}

	// 6. Verify Magnets: quality extracted, priority_score computed (中字8 + 4K2 + 有码1 = 11)
	magnetRepo := NewMagnetRepo(database)
	mags, err := magnetRepo.ListMagnetsByMovie(ctx, "IPX-123")
	if err != nil || len(mags) != 1 {
		t.Fatalf("ListMagnetsByMovie failed: len=%d, err=%v", len(mags), err)
	}
	mag := mags[0]
	if mag.PriorityScore != 11 {
		t.Errorf("expected priority_score=11, got %d", mag.PriorityScore)
	}
	if mag.SizeBytes != 2048*1048576 {
		t.Errorf("expected size_bytes=%d, got %d", 2048*1048576, mag.SizeBytes)
	}
	if mag.IsPreferred != 0 {
		t.Errorf("expected is_preferred=0 after migration, got %d", mag.IsPreferred)
	}

	// 7. Verify libraries and settings exist
	libRepo := NewLibraryRepo(database)
	libs, err := libRepo.ListLibraries(ctx)
	if err != nil || len(libs) != 6 {
		t.Fatalf("expected 6 default libraries, got %d (err=%v)", len(libs), err)
	}
}
