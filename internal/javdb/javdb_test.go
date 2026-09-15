package javdb

import (
	"context"
	"encoding/json"
	"mediavault/internal/db"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

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

func setupMockJavDBServer() *httptest.Server {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")

		switch path {
		case "/api/v2/search":
			q := r.URL.Query().Get("q")
			if q == "SSIS-123" {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"data": map[string]interface{}{
						"movies": []map[string]interface{}{
							{
								"id":        "mov_123",
								"number":    "SSIS-123",
								"title":     "SSIS-123 Title",
								"cover_url": "https://img.javdb.com/cover123.jpg",
								"score":     4.5,
							},
						},
					},
				})
			} else if q == "NOTFOUND-999" {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"success": true,
					"data": map[string]interface{}{
						"movies": []interface{}{},
					},
				})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"success": false,
					"message": "Not Found",
				})
			}

		case "/api/v2/movies/mov_123":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"movie": map[string]interface{}{
						"id":             "mov_123",
						"number":         "SSIS-123",
						"title":          "SSIS-123 Full Title",
						"title_zh":       "SSIS-123 中文标题",
						"description_zh": "这是中文剧情简介",
						"cover_url":      "https://img.javdb.com/cover123.jpg",
						"score":          "4.5",
						"release_date":   "2026-01-01",
						"duration":       120,
						"type":           "censored",
						"maker_name":     "S1",
						"director_name":  "Director X",
						"actors":         []map[string]interface{}{{"name": "Actor A"}, {"name": "Actor B"}},
						"tags":           []map[string]interface{}{{"name": "Tag 1"}, {"name": "Tag 2"}},
					},
				},
			})

		case "/api/v2/movies/mov_partial":
			// Missing title_zh and description_zh
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"movie": map[string]interface{}{
						"id":     "mov_partial",
						"number": "PART-001",
						"title":  "PART-001 Title",
					},
				},
			})

		case "/api/v1/movies/mov_123/magnets":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"magnets": []map[string]interface{}{
						{
							"name":  "SSIS-123-C 1080P",
							"hash":  "1111222233334444555566667777888899990000",
							"size":  1907,
							"hd":    true,
							"cnsub": true,
						},
					},
				},
			})

		case "/api/v1/movies/mov_123/reviews":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"reviews": []map[string]interface{}{
						{
							"content": "好片分享: magnet:?xt=urn:btih:AAAA222233334444555566667777888899990000 以及 ed2k://|file|sample.mp4|104857600|FEDCBA9876543210FEDCBA9876543210|/",
						},
					},
				},
			})

		case "/api/v1/rankings":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"data": map[string]interface{}{
					"movies": []map[string]interface{}{
						{
							"id":         "mov_123",
							"number":     "SSIS-123",
							"title":      "SSIS-123 Ranking Title",
							"video_type": "censored",
						},
						{
							"id":         "mov_vr",
							"number":     "VR-999",
							"title":      "VR Video Excluded",
							"video_type": "vr",
						},
					},
				},
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	return httptest.NewServer(handler)
}

func TestSignatureCalculation(t *testing.T) {
	sig := BuildSignature()
	if sig == "" {
		t.Fatalf("expected non-empty signature")
	}
	parts := len(splitDelim(sig, '.'))
	if parts != 3 {
		t.Errorf("expected 3 parts in signature, got %d (%s)", parts, sig)
	}
}

func splitDelim(s string, delim rune) []string {
	var res []string
	curr := ""
	for _, r := range s {
		if r == delim {
			res = append(res, curr)
			curr = ""
		} else {
			curr += string(r)
		}
	}
	res = append(res, curr)
	return res
}

func TestCircuitBreaker(t *testing.T) {
	cb := NewCircuitBreaker(100 * time.Millisecond)

	// Initially closed
	if err := cb.Allow(); err != nil {
		t.Fatalf("expected allow on initial closed breaker, got %v", err)
	}

	// 3 consecutive risk control errors trip the breaker
	cb.RecordFailure(true)
	cb.RecordFailure(true)
	cb.RecordFailure(true)

	if err := cb.Allow(); err != ErrCircuitOpen {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}

	// Wait for cooldown
	time.Sleep(120 * time.Millisecond)

	// Half-open allows 1 probe
	if err := cb.Allow(); err != nil {
		t.Fatalf("expected probe allowed after cooldown, got %v", err)
	}
	// Second concurrent call in half-open rejected
	if err := cb.Allow(); err != ErrCircuitOpen {
		t.Fatalf("expected second probe rejected in half-open, got %v", err)
	}

	// Success closes the breaker
	cb.RecordSuccess()
	if err := cb.Allow(); err != nil {
		t.Fatalf("expected allow after success, got %v", err)
	}
}

func TestScraperSuccessAndProvenance(t *testing.T) {
	mockServer := setupMockJavDBServer()
	defer mockServer.Close()

	database, _ := setupTestDB(t)
	defer database.Close()

	ctx := context.Background()

	// Insert movie
	_, _ = database.Writer().ExecContext(ctx, `
		INSERT INTO offline_movies (
			code, title, category, first_seen_at, source_websites, actors, tags,
			is_enriched, scrape_policy, metadata_sources, manual_fields, legacy_metadata,
			created_at, updated_at
		) VALUES ('SSIS-123', 'Old Title', '亚洲有码', '2026-01-01T00:00:00Z', '["test"]', '[]', '[]', 0, 'auto', '{}', '["title"]', '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
	`)

	client := NewClient(ClientConfig{
		BaseURL:            mockServer.URL,
		RequestIntervalSec: 0.01,
		DelayMin:           0.01,
		DelayMax:           0.02,
	})

	scraper := NewScraper(database, client, nil)
	res, err := scraper.ScrapeMovie(ctx, "SSIS-123")
	if err != nil {
		t.Fatalf("ScrapeMovie failed: %v", err)
	}

	if res.Status != "success" || res.IsEnriched != 1 {
		t.Errorf("expected success with is_enriched=1, got status=%s, enriched=%d", res.Status, res.IsEnriched)
	}

	// Verify manual lock on 'title' was preserved
	var title, titleZH, descZH string
	var score float64
	_ = database.Reader().QueryRowContext(ctx, "SELECT title, title_zh, description_zh, score FROM offline_movies WHERE code = 'SSIS-123'").Scan(&title, &titleZH, &descZH, &score)

	if title != "Old Title" {
		t.Errorf("manual lock violated: title changed to %s", title)
	}
	if titleZH != "SSIS-123 中文标题" {
		t.Errorf("expected title_zh 'SSIS-123 中文标题', got %s", titleZH)
	}
	if descZH != "这是中文剧情简介" {
		t.Errorf("expected descZH, got %s", descZH)
	}
	if score != 4.5 {
		t.Errorf("expected score 4.5, got %f", score)
	}

	// Verify magnets were inserted (both official and review)
	magRepo := db.NewMagnetRepo(database)
	magnets, err := magRepo.ListMagnetsByMovie(ctx, "SSIS-123")
	if err != nil {
		t.Fatalf("ListMagnetsByMovie failed: %v", err)
	}
	if len(magnets) < 2 {
		t.Fatalf("expected at least 2 magnets (1 official, 1+ review), got %d", len(magnets))
	}
}

func TestScraperNotFoundStateMachine(t *testing.T) {
	mockServer := setupMockJavDBServer()
	defer mockServer.Close()

	database, _ := setupTestDB(t)
	defer database.Close()

	ctx := context.Background()

	// Insert movie with first_seen_at 15 days ago
	oldDate := time.Now().UTC().Add(-15 * 24 * time.Hour).Format(time.RFC3339)
	_, _ = database.Writer().ExecContext(ctx, `
		INSERT INTO offline_movies (
			code, title, category, first_seen_at, source_websites, actors, tags,
			is_enriched, scrape_policy, scrape_status, not_found_count, last_not_found_day,
			metadata_sources, manual_fields, legacy_metadata, created_at, updated_at
		) VALUES ('NOTFOUND-999', 'Old NF Title', '亚洲有码', ?, '["test"]', '[]', '[]', 0, 'auto', 'idle', 2, '2026-01-01', '{}', '[]', '{}', ?, ?);
	`, oldDate, oldDate, oldDate)

	client := NewClient(ClientConfig{
		BaseURL:            mockServer.URL,
		RequestIntervalSec: 0.01,
		DelayMin:           0.01,
		DelayMax:           0.02,
	})

	scraper := NewScraper(database, client, nil)
	res, err := scraper.ScrapeMovie(ctx, "NOTFOUND-999")
	if err != nil {
		t.Fatalf("ScrapeMovie failed: %v", err)
	}

	if res.Status != "not_found" {
		t.Fatalf("expected not_found, got %s", res.Status)
	}

	// Since previous not_found_count was 2, today is different, and first_seen > 10 days, it should transition to paused
	var policy, reason string
	var count int
	_ = database.Reader().QueryRowContext(ctx, "SELECT scrape_policy, policy_reason, not_found_count FROM offline_movies WHERE code = 'NOTFOUND-999'").Scan(&policy, &reason, &count)

	if count != 3 {
		t.Errorf("expected not_found_count 3, got %d", count)
	}
	if policy != "paused" || reason != "not_found" {
		t.Errorf("expected policy paused/not_found, got %s/%s", policy, reason)
	}
}

func TestSearchServiceKeywordVsCode(t *testing.T) {
	mockServer := setupMockJavDBServer()
	defer mockServer.Close()

	database, _ := setupTestDB(t)
	defer database.Close()

	ctx := context.Background()
	movieRepo := db.NewMovieRepo(database)
	jobRepo := db.NewJobRepo(database)
	client := NewClient(ClientConfig{
		BaseURL:            mockServer.URL,
		RequestIntervalSec: 0.01,
		DelayMin:           0.01,
		DelayMax:           0.02,
	})
	scraper := NewScraper(database, client, nil)
	searchService := NewSearchService(database, movieRepo, jobRepo, scraper, nil)

	// 1. Ordinary keyword search: should not hit JavDB
	res, err := searchService.Search(ctx, "Ordinary Keyword", 10, 0)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(res.Movies) != 0 {
		t.Errorf("expected empty movies for ordinary keyword without local matches, got %d", len(res.Movies))
	}

	// 2. Exact code search: should hit JavDB and import movie
	resCode, err := searchService.Search(ctx, "ssis-123", 10, 0)
	if err != nil {
		t.Fatalf("Search code failed: %v", err)
	}
	if len(resCode.Movies) == 0 {
		t.Fatalf("expected imported movie for code ssis-123, got 0")
	}
	if resCode.Movies[0].Code != "SSIS-123" {
		t.Errorf("expected code SSIS-123, got %s", resCode.Movies[0].Code)
	}
}
