package ingestion

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"mediavault/internal/db"
	"mediavault/internal/identity"
	"mediavault/internal/models"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

// Helper to create an encrypted AVDB zip archive for testing
func createTestEncryptedZip(t *testing.T, csvContent string, iterations int, corruptTag bool) []byte {
	// 1. Create inner zip containing CSV
	var innerBuf bytes.Buffer
	innerZip := zip.NewWriter(&innerBuf)
	f, err := innerZip.Create("data.csv")
	if err != nil {
		t.Fatalf("create inner zip file: %v", err)
	}
	if _, err := f.Write([]byte(csvContent)); err != nil {
		t.Fatalf("write inner zip: %v", err)
	}
	if err := innerZip.Close(); err != nil {
		t.Fatalf("close inner zip: %v", err)
	}
	innerBytes := innerBuf.Bytes()

	// 2. Encrypt inner zip with PBKDF2 + AES-GCM
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	_, _ = rand.Read(salt)
	_, _ = rand.Read(nonce)

	key := pbkdf2.Key(DefaultResourceLibraryPasswordDigest, salt, iterations, 32, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("new gcm: %v", err)
	}

	sealed := gcm.Seal(nil, nonce, innerBytes, nil)
	ciphertext := sealed[:len(sealed)-16]
	tag := sealed[len(sealed)-16:]

	if corruptTag {
		tag[0] ^= 0xff // corrupt authentication tag
	}

	manifest := Manifest{
		Payload:    "avdb-resource-library.bin",
		Salt:       base64.StdEncoding.EncodeToString(salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Tag:        base64.StdEncoding.EncodeToString(tag),
		Iterations: iterations,
	}
	manifestBytes, _ := json.Marshal(manifest)

	// 3. Create outer zip
	var outerBuf bytes.Buffer
	outerZip := zip.NewWriter(&outerBuf)

	mFile, err := outerZip.Create("avdb-resource-library.json")
	if err != nil {
		t.Fatalf("create manifest: %v", err)
	}
	_, _ = mFile.Write(manifestBytes)

	pFile, err := outerZip.Create("avdb-resource-library.bin")
	if err != nil {
		t.Fatalf("create payload: %v", err)
	}
	_, _ = pFile.Write(ciphertext)

	if err := outerZip.Close(); err != nil {
		t.Fatalf("close outer zip: %v", err)
	}

	return outerBuf.Bytes()
}

func setupTestDB(t *testing.T) (*db.DB, string) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if _, err := db.InitEmptyDatabase(context.Background(), database); err != nil {
		t.Fatalf("failed to init schema: %v", err)
	}

	return database, tempDir
}

func TestDecryptAVDBArchive(t *testing.T) {
	csvContent := "number,magnet,title,section\nSSIS-123,magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567,Test Title,中文字幕\n"

	// 1. Success decryption
	zipData := createTestEncryptedZip(t, csvContent, 200000, false)
	cfg := DefaultDecryptConfig()
	fname, decrypted, err := DecryptAVDBArchive(zipData, cfg)
	if err != nil {
		t.Fatalf("expected successful decrypt, got error: %v", err)
	}
	if fname != "data.csv" {
		t.Errorf("expected filename data.csv, got %s", fname)
	}
	if string(decrypted) != csvContent {
		t.Errorf("decrypted content mismatch: %s", string(decrypted))
	}

	// 2. Authentication tag failure
	corruptZip := createTestEncryptedZip(t, csvContent, 200000, true)
	_, _, err = DecryptAVDBArchive(corruptZip, cfg)
	if err == nil {
		t.Fatalf("expected decryption error on corrupt tag, got nil")
	}

	// 3. Iteration limit exceeded
	highIterZip := createTestEncryptedZip(t, csvContent, 1500000, false)
	_, _, err = DecryptAVDBArchive(highIterZip, cfg)
	if err == nil {
		t.Fatalf("expected iteration limit error, got nil")
	}

	// 4. Archive budget exceeded
	smallBudgetCfg := cfg
	smallBudgetCfg.ArchiveMaxBytes = 100
	_, _, err = DecryptAVDBArchive(zipData, smallBudgetCfg)
	if err == nil {
		t.Fatalf("expected archive max bytes budget error, got nil")
	}
}

func TestNormalizeSectionAndCategory(t *testing.T) {
	tests := []struct {
		sec        string
		title      string
		expectedCat string
		isExcluded bool
		isExempt   bool
	}{
		{"VR视频区", "VR Demo", CategoryExcluded, true, false},
		{"欧美无码", "Western movie", CategoryExcluded, true, false},
		{"三级写真", "Photo collection", CategoryExcluded, true, false},
		{"中文字幕", "SSNI-123", "中文字幕", false, false},
		{"高清中文字幕", "SSNI-123", "中文字幕", false, false},
		{"FC2", "FC2-PPV-1234567", "FC2/素人", false, true},
		{"国产", "主播精选 01", "国产", false, true},
		{"未知", "FC2-1234567 SIRO", "FC2/素人", false, true},
		{"未知", "SSNI-999-C 4K 中文字幕", "中文字幕", false, false},
		{"亚洲无码", "Caribbeancom 123", "亚洲无码", false, false},
	}

	for _, tc := range tests {
		cat, excluded, exempt := NormalizeSection(tc.sec, tc.title)
		if cat != tc.expectedCat || excluded != tc.isExcluded || exempt != tc.isExempt {
			t.Errorf("NormalizeSection(%q, %q) = (%q, %v, %v); expected (%q, %v, %v)",
				tc.sec, tc.title, cat, excluded, exempt, tc.expectedCat, tc.isExcluded, tc.isExempt)
		}
	}
}

func TestCSVParserAndRowNormalization(t *testing.T) {
	csvData := []byte("\xef\xbb\xbfnumber,magnet,title,section,publish_date,size\n" +
		"ssis-123,magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567,SSIS-123 中文字幕 4K,中文字幕,2026-01-01,4500\n" +
		"fc2-ppv-1234567,magnet:?xt=urn:btih:fedcba9876543210fedcba9876543210fedcba98,FC2-PPV-1234567 素人,FC2,2026-01-02,2048\n" +
		"vr-001,magnet:?xt=urn:btih:1111111111111111111111111111111111111111,VR Video,VR视频区,2026-01-03,1000\n" +
		"corrupt_row_no_magnet\n")

	var rows []RawCSVRow
	err := ParseCSVStream(csvData, "sehuatang", func(raw RawCSVRow) error {
		rows = append(rows, raw)
		return nil
	})
	if err != nil {
		t.Fatalf("ParseCSVStream failed: %v", err)
	}

	if len(rows) != 3 {
		t.Fatalf("expected 3 valid parsed rows, got %d", len(rows))
	}

	// First row (SSIS-123)
	rec1, err := ParseRawRow(rows[0])
	if err != nil || rec1 == nil {
		t.Fatalf("ParseRawRow row 0 failed: %v", err)
	}
	if rec1.Code != "SSIS-123" {
		t.Errorf("expected code SSIS-123, got %s", rec1.Code)
	}
	if rec1.ScrapePolicy != "auto" {
		t.Errorf("expected auto scrape policy, got %s", rec1.ScrapePolicy)
	}
	if rec1.Quality.HasChineseSub != 1 || rec1.Quality.Is4K != 1 {
		t.Errorf("expected chinese sub and 4k flags, got %+v", rec1.Quality)
	}

	// Second row (FC2)
	rec2, err := ParseRawRow(rows[1])
	if err != nil || rec2 == nil {
		t.Fatalf("ParseRawRow row 1 failed: %v", err)
	}
	if rec2.Code != "FC2-PPV-1234567" {
		t.Errorf("expected code FC2-PPV-1234567, got %s", rec2.Code)
	}
	if rec2.ScrapePolicy != "exempt" {
		t.Errorf("expected exempt scrape policy, got %s", rec2.ScrapePolicy)
	}

	// Third row (VR - should be excluded)
	rec3, err := ParseRawRow(rows[2])
	if err != nil {
		t.Fatalf("ParseRawRow row 2 returned error: %v", err)
	}
	if rec3 != nil {
		t.Errorf("expected VR row to be excluded (nil), got %+v", rec3)
	}
}

func TestBatchMergeAndCrossMovieConflict(t *testing.T) {
	pool, tempDir := setupTestDB(t)
	defer pool.Close()
	defer os.RemoveAll(tempDir)

	ctx := context.Background()

	// 1. Prepare records for Batch 1
	rec1 := &IngestRecord{
		Code:          "MIDV-001",
		Title:         "MIDV-001 First Title",
		Category:      "中文字幕",
		SourceWebsite: "sehuatang",
		ScrapePolicy:  "auto",
		ResourceKind:  "btih",
		ResourceKey:   "AAAA000011112222333344445555666677778888",
		MagnetURL:     "magnet:?xt=urn:btih:AAAA000011112222333344445555666677778888",
		Quality: identity.QualityFlags{
			HasChineseSub: 1,
			Score:         8,
		},
		QualityLabel: "1080P",
		SizeBytes:    2000000000,
	}

	tx1, _ := pool.Writer().BeginTx(ctx, nil)
	stats1, err := MergeBatchTx(ctx, tx1, []*IngestRecord{rec1})
	if err != nil {
		t.Fatalf("Batch 1 merge failed: %v", err)
	}
	_ = tx1.Commit()

	if stats1.MoviesInserted != 1 || stats1.MagnetsInserted != 1 {
		t.Errorf("expected 1 movie inserted, 1 magnet inserted, got %+v", stats1)
	}

	// 2. Cross-movie conflict: same info_hash with a different movie code (MIDV-002)
	recConflict := &IngestRecord{
		Code:          "MIDV-002",
		Title:         "MIDV-002 Competing Title",
		Category:      "亚洲有码",
		SourceWebsite: "x1080x",
		ScrapePolicy:  "auto",
		ResourceKind:  "btih",
		ResourceKey:   "AAAA000011112222333344445555666677778888", // duplicate hash!
		MagnetURL:     "magnet:?xt=urn:btih:AAAA000011112222333344445555666677778888",
		Quality:       identity.QualityFlags{},
		QualityLabel:  "1080P",
		SizeBytes:     2000000000,
	}

	tx2, _ := pool.Writer().BeginTx(ctx, nil)
	stats2, err := MergeBatchTx(ctx, tx2, []*IngestRecord{recConflict})
	if err != nil {
		t.Fatalf("Batch 2 merge failed: %v", err)
	}
	_ = tx2.Commit()

	if len(stats2.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict recorded, got %d", len(stats2.Conflicts))
	}
	if stats2.Conflicts[0].ExistingCode != "MIDV-001" || stats2.Conflicts[0].IncomingCode != "MIDV-002" {
		t.Errorf("conflict mismatch: %+v", stats2.Conflicts[0])
	}

	// Verify magnet movie_code was NOT reassigned
	var boundCode string
	_ = pool.Reader().QueryRowContext(ctx, "SELECT movie_code FROM offline_magnets WHERE info_hash = ?", "AAAA000011112222333344445555666677778888").Scan(&boundCode)
	if boundCode != "MIDV-001" {
		t.Errorf("expected magnet to stay bound to MIDV-001, got %s", boundCode)
	}
}

func TestManualLockProtection(t *testing.T) {
	pool, tempDir := setupTestDB(t)
	defer pool.Close()
	defer os.RemoveAll(tempDir)

	ctx := context.Background()

	// Insert movie with manual lock on category
	_, err := pool.Writer().ExecContext(ctx, `
		INSERT INTO offline_movies (
			code, title, category, first_seen_at, source_websites, actors, tags,
			is_enriched, scrape_policy, metadata_sources, manual_fields, legacy_metadata,
			created_at, updated_at
		) VALUES (
			'STAR-777', 'Manual Title', '亚洲有码', '2026-01-01T00:00:00Z', '["sehuatang"]',
			'[]', '[]', 1, 'exempt', '{}', '["category", "scrape_policy"]', '{}',
			'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'
		);
	`)
	if err != nil {
		t.Fatalf("insert movie failed: %v", err)
	}

	// Incoming record wants to change category to "中文字幕" and policy to "auto"
	incoming := &IngestRecord{
		Code:          "STAR-777",
		Title:         "Incoming Title",
		Category:      "中文字幕",
		SourceWebsite: "x1080x",
		ScrapePolicy:  "auto",
		ResourceKind:  "btih",
		ResourceKey:   "BBBB000011112222333344445555666677778888",
		MagnetURL:     "magnet:?xt=urn:btih:BBBB000011112222333344445555666677778888",
		Quality:       identity.QualityFlags{},
		QualityLabel:  "1080P",
		SizeBytes:     1000,
	}

	tx, _ := pool.Writer().BeginTx(ctx, nil)
	_, err = MergeBatchTx(ctx, tx, []*IngestRecord{incoming})
	if err != nil {
		t.Fatalf("MergeBatchTx failed: %v", err)
	}
	_ = tx.Commit()

	var cat, policy, websites string
	_ = pool.Reader().QueryRowContext(ctx, "SELECT category, scrape_policy, source_websites FROM offline_movies WHERE code = 'STAR-777'").Scan(&cat, &policy, &websites)

	if cat != "亚洲有码" {
		t.Errorf("manual lock violated: category changed to %s", cat)
	}
	if policy != "exempt" {
		t.Errorf("manual lock violated: scrape_policy changed to %s", policy)
	}
	// Websites should be merged
	if websites != `["sehuatang","x1080x"]` {
		t.Errorf("expected merged websites [sehuatang, x1080x], got %s", websites)
	}
}

func TestSyncWatermarkProgressionAndGap(t *testing.T) {
	pool, tempDir := setupTestDB(t)
	defer pool.Close()
	defer os.RemoveAll(tempDir)

	ctx := context.Background()
	ingestRepo := db.NewIngestRepo(pool)

	// Initially no watermark
	wm, err := ingestRepo.GetSyncWatermark(ctx)
	if err != nil {
		t.Fatalf("GetSyncWatermark failed: %v", err)
	}
	if wm != nil {
		t.Errorf("expected nil watermark on empty db, got %+v", wm)
	}

	jobRepo := db.NewJobRepo(pool)
	_, _, err = jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:        "run_1",
		Kind:      "sync30d",
		DedupeKey: "sync30d:2026-09-01-00-00-00",
		State:     "running",
	})
	if err != nil {
		t.Fatalf("create job run_1 failed: %v", err)
	}

	// Record asset 1 (sehuatang) as completed
	err = ingestRepo.UpsertIngestAsset(ctx, &models.IngestAsset{
		ID:        "ast_1",
		RunID:     "run_1",
		ReleaseID: "2026-09-01-00-00-00",
		Source:    "30D_sehuatang",
		AssetName: "30D_sehuatang.zip",
		SHA256:    "hash1",
		SizeBytes: 1000,
		State:     "completed",
	})

	// Check if both sources completed
	done, err := ingestRepo.AreReleaseAssetsCompleted(ctx, "2026-09-01-00-00-00", []string{"30D_sehuatang", "30D_X1080X"})
	if err != nil {
		t.Fatalf("AreReleaseAssetsCompleted failed: %v", err)
	}
	if done {
		t.Errorf("expected false because X1080X is missing")
	}

	// Record asset 2 (X1080X) as completed
	_ = ingestRepo.UpsertIngestAsset(ctx, &models.IngestAsset{
		ID:        "ast_2",
		RunID:     "run_1",
		ReleaseID: "2026-09-01-00-00-00",
		Source:    "30D_X1080X",
		AssetName: "30D_X1080X.zip",
		SHA256:    "hash2",
		SizeBytes: 2000,
		State:     "completed",
	})

	done, err = ingestRepo.AreReleaseAssetsCompleted(ctx, "2026-09-01-00-00-00", []string{"30D_sehuatang", "30D_X1080X"})
	if err != nil || !done {
		t.Fatalf("expected true when both completed, got done=%v, err=%v", done, err)
	}

	// Update watermark
	err = ingestRepo.UpdateSyncWatermark(ctx, &db.SyncWatermark{
		ReleaseID:    "2026-09-01-00-00-00",
		CoverageDate: "2026-09-01",
		Sources:      []string{"30D_sehuatang", "30D_X1080X"},
		SyncGap:      false,
	})
	if err != nil {
		t.Fatalf("UpdateSyncWatermark failed: %v", err)
	}

	wm, err = ingestRepo.GetSyncWatermark(ctx)
	if err != nil || wm == nil {
		t.Fatalf("GetSyncWatermark failed: %v", err)
	}
	if wm.CoverageDate != "2026-09-01" {
		t.Errorf("expected coverage date 2026-09-01, got %s", wm.CoverageDate)
	}
}
