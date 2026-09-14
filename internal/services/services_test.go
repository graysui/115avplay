package services

import (
	"context"
	"encoding/json"
	"mediavault/internal/client115"
	"mediavault/internal/db"
	"mediavault/internal/models"
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

func setupMock115Server() *httptest.Server {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")

		switch path {
		case "/open/ufile/files":
			if r.URL.Query().Get("cid") == "cid_temp_root" {
				_ = json.NewEncoder(w).Encode(client115.BaseResponse{
					State: true,
					Code:  0,
					Data:  json.RawMessage(`{"count":0,"data":[]}`),
				})
				return
			}
			_ = json.NewEncoder(w).Encode(client115.BaseResponse{
				State: true,
				Code:  0,
				Data: json.RawMessage(`{
					"count": 3,
					"data": [
						{
							"fid": "file_1001",
							"fn": "SSIS-123_CH_4K.mp4",
							"pc": "pick_1001",
							"fs": 2147483648,
							"cid": "cid_root"
						},
						{
							"fid": "file_sample",
							"fn": "sample_video.mp4",
							"pc": "pick_sample",
							"fs": 50000000,
							"cid": "cid_root"
						},
						{
							"fid": "file_unsupported",
							"fn": "movie.iso",
							"pc": "pick_iso",
							"fs": 4000000000,
							"cid": "cid_root"
						}
					]
				}`),
			})

		case "/open/ufile/downurl":
			_ = json.NewEncoder(w).Encode(client115.BaseResponse{
				State: true,
				Code:  0,
				Data: json.RawMessage(`{
					"pick_1001": {
						"url": "https://cdn.115.com/download/SSIS-123.mp4?sign=valid",
						"file_size": 2147483648,
						"file_name": "SSIS-123_CH_4K.mp4",
						"file_id": "file_1001",
						"pick_code": "pick_1001"
					}
				}`),
			})

		case "/open/ufile/delete":
			_ = json.NewEncoder(w).Encode(client115.BaseResponse{
				State: true,
				Code:  0,
			})

		case "/open/folder/add", "/open/user/files/add":
			_ = json.NewEncoder(w).Encode(client115.BaseResponse{
				State: true,
				Code:  0,
				Data:  json.RawMessage(`{"file_id":"folder_tmp_123","cid":"folder_tmp_123"}`),
			})

		case "/open/offline/add_task_bt":
			_ = json.NewEncoder(w).Encode(client115.BaseResponse{
				State: true,
				Code:  0,
				Data:  json.RawMessage(`{"info_hash":"HASH_COLD_001"}`),
			})

		case "/open/offline/get_task_list":
			_ = json.NewEncoder(w).Encode(client115.BaseResponse{
				State: true,
				Code:  0,
				Data: json.RawMessage(`{
					"count": 1,
					"tasks": [
						{
							"info_hash": "HASH_COLD_001",
							"name": "SSIS-123.mp4",
							"size": 2000000000,
							"percentDone": 100.0,
							"status": 2,
							"file_id": "file_1001"
						}
					]
				}`),
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	return httptest.NewServer(handler)
}

func TestPlaybackPredicates(t *testing.T) {
	now := time.Now().UTC()
	futureTime := now.Add(2 * time.Hour).Format(time.RFC3339)
	pastTime := now.Add(-2 * time.Hour).Format(time.RFC3339)

	fileID := "fid_1"
	pickCode := "pc_1"

	// 1. Permanent ready asset: playable
	permAsset := &models.CloudAsset{
		SourceType: "permanent",
		State:      "ready",
		FileID:     &fileID,
		PickCode:   &pickCode,
		ExpiresAt:  nil,
	}
	if !IsAssetPlayable(permAsset, now) {
		t.Errorf("expected permanent ready asset to be playable")
	}

	// 2. Temporary ready asset with future expires_at: playable
	tempAssetValid := &models.CloudAsset{
		SourceType: "temporary",
		State:      "ready",
		FileID:     &fileID,
		PickCode:   &pickCode,
		ExpiresAt:  &futureTime,
	}
	if !IsAssetPlayable(tempAssetValid, now) {
		t.Errorf("expected temporary asset with future expiry to be playable")
	}

	// 3. Temporary ready asset with past expires_at: not playable for new session
	tempAssetExpired := &models.CloudAsset{
		SourceType: "temporary",
		State:      "ready",
		FileID:     &fileID,
		PickCode:   &pickCode,
		ExpiresAt:  &pastTime,
	}
	if IsAssetPlayable(tempAssetExpired, now) {
		t.Errorf("expected temporary asset with past expiry to NOT be playable for new session")
	}

	// 4. Ongoing session with active play lease can continue on expired asset
	sessionLease := futureTime
	activeSession := &models.PlaySession{
		State:      "active",
		LeaseUntil: &sessionLease,
	}
	if !CanSessionContinue(activeSession, tempAssetExpired, now) {
		t.Errorf("expected ongoing active session with valid lease to continue on expired asset")
	}
}

func TestTreeScannerPermanentReconciliation(t *testing.T) {
	mockServer := setupMock115Server()
	defer mockServer.Close()

	database, _ := setupTestDB(t)
	defer database.Close()

	ctx := context.Background()

	// 1. Insert active binding and movie
	assetRepo := db.NewAssetRepo(database)
	_ = assetRepo.UpsertBinding(ctx, &models.CloudBinding{
		ID:               "binding_test_115",
		Provider:         "115",
		ProviderUserID:   "user_115",
		Enabled:          1,
		SecretSettingKey: "provider_tokens:binding_test_115",
	})

	movieRepo := db.NewMovieRepo(database)
	_ = movieRepo.UpsertMovie(ctx, &models.Movie{
		Code:        "SSIS-123",
		Title:       "SSIS-123 Title",
		Category:    "亚洲有码",
		FirstSeenAt: models.UTCNow(),
	})

	c115Client, _ := client115.NewClient(client115.ClientConfig{BaseURL: mockServer.URL})
	magnetRepo := db.NewMagnetRepo(database)
	jobRepo := db.NewJobRepo(database)

	scanner := NewTreeScanner(database, assetRepo, movieRepo, magnetRepo, jobRepo, c115Client, nil)

	stats, err := scanner.ScanCID(ctx, "cid_root", "full")
	if err != nil {
		t.Fatalf("ScanCID failed: %v", err)
	}

	if stats.ValidVideos != 1 || stats.MatchedMovies != 1 || stats.AssetsCreated != 1 {
		t.Errorf("expected 1 valid video, 1 matched, 1 asset created, got %+v", stats)
	}

	// Verify magnet and asset exist
	resourceKey := "115:binding_test_115:file_1001"
	asset, err := assetRepo.GetReadyAssetByResource(ctx, resourceKey, "binding_test_115", models.UTCNow())
	if err != nil || asset == nil {
		t.Fatalf("expected ready asset for scanned file, got nil")
	}
	if asset.SourceType != "permanent" || asset.State != "ready" {
		t.Errorf("expected permanent ready asset, got %+v", asset)
	}
}

func TestJanitorCleanupWithPlayLease(t *testing.T) {
	mockServer := setupMock115Server()
	defer mockServer.Close()

	database, _ := setupTestDB(t)
	defer database.Close()

	ctx := context.Background()
	assetRepo := db.NewAssetRepo(database)

	movieRepo := db.NewMovieRepo(database)
	_ = movieRepo.UpsertMovie(ctx, &models.Movie{
		Code:        "SSIS-123",
		Title:       "SSIS-123",
		Category:    "亚洲有码",
		FirstSeenAt: models.UTCNow(),
	})

	_, _ = database.Writer().ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, is_admin, enabled, must_change_password, created_at, updated_at)
		VALUES ('u1', 'user1', 'hash', 1, 1, 0, ?, ?);
	`, models.UTCNow(), models.UTCNow())

	_ = assetRepo.UpsertBinding(ctx, &models.CloudBinding{
		ID:               "binding_1",
		Provider:         "115",
		ProviderUserID:   "u_1",
		Enabled:          1,
		SecretSettingKey: "sec",
	})

	c115Client, _ := client115.NewClient(client115.ClientConfig{BaseURL: mockServer.URL})
	janitor := NewJanitorService(database, assetRepo, c115Client, "cid_temp_root", nil)

	jobRepo := db.NewJobRepo(database)
	_, _, err := jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:        "job_transfer_test",
		Kind:      "transfer",
		DedupeKey: "transfer:binding_1:test",
		BindingID: stringPtr("binding_1"),
	})
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	// Create expired asset 1 (protected by active lease)
	now := time.Now().UTC()
	pastReady := now.Add(-2 * time.Hour).Format(time.RFC3339)
	pastExpiry := now.Add(-1 * time.Hour).Format(time.RFC3339)
	futureLease := now.Add(1 * time.Hour).Format(time.RFC3339)
	folder1 := "cid_folder_1"
	fileID1 := "fid_1"
	pick1 := "pick_1"

	err = assetRepo.UpsertAsset(ctx, &models.CloudAsset{
		ID:           "ast_expired_1",
		BindingID:    "binding_1",
		OwningJobID:  stringPtr("job_transfer_test"),
		SourceType:   "temporary",
		State:        "ready",
		OwnedRootID:  &folder1,
		RootSnapshot: stringPtr("cid_folder_1"),
		ReadyAt:      &pastReady,
		FileID:       &fileID1,
		PickCode:     &pick1,
		ExpiresAt:    &pastExpiry,
	})
	if err != nil {
		t.Fatalf("failed to insert ast_expired_1: %v", err)
	}

	// Create active play session on ast_expired_1
	_, err = database.Writer().ExecContext(ctx, `
		INSERT INTO play_sessions (id, user_id, movie_code, asset_id, device_id, state, lease_until, updated_at)
		VALUES ('sess_1', 'u1', 'SSIS-123', 'ast_expired_1', 'dev1', 'active', ?, ?);
	`, futureLease, models.UTCNow())
	if err != nil {
		t.Fatalf("failed to insert play session: %v", err)
	}

	// Create expired asset 2 (unprotected, should be cleaned)
	folder2 := "cid_folder_2"
	fileID2 := "fid_2"
	pick2 := "pick_2"
	err = assetRepo.UpsertAsset(ctx, &models.CloudAsset{
		ID:           "ast_expired_2",
		BindingID:    "binding_1",
		OwningJobID:  stringPtr("job_transfer_test"),
		SourceType:   "temporary",
		State:        "ready",
		OwnedRootID:  &folder2,
		RootSnapshot: stringPtr("cid_folder_2"),
		ReadyAt:      &pastReady,
		FileID:       &fileID2,
		PickCode:     &pick2,
		ExpiresAt:    &pastExpiry,
	})
	if err != nil {
		t.Fatalf("failed to insert ast_expired_2: %v", err)
	}

	cleaned, skipped, err := janitor.RunCleanup(ctx, true)
	if err != nil {
		t.Fatalf("RunCleanup failed: %v", err)
	}

	if skipped != 1 {
		t.Errorf("expected 1 asset skipped due to active play lease, got %d", skipped)
	}
	if cleaned != 1 {
		t.Errorf("expected 1 asset cleaned, got %d", cleaned)
	}

	// Verify asset 2 is deleted
	a2, _ := assetRepo.GetAssetByID(ctx, "ast_expired_2")
	if a2.State != "deleted" {
		t.Errorf("expected asset 2 state 'deleted', got %s", a2.State)
	}
}

func TestResolverPlaybackHierarchy(t *testing.T) {
	mockServer := setupMock115Server()
	defer mockServer.Close()

	database, _ := setupTestDB(t)
	defer database.Close()

	ctx := context.Background()
	assetRepo := db.NewAssetRepo(database)
	movieRepo := db.NewMovieRepo(database)
	magnetRepo := db.NewMagnetRepo(database)
	jobRepo := db.NewJobRepo(database)

	_, _ = database.Writer().ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, is_admin, enabled, must_change_password, created_at, updated_at)
		VALUES ('user_1', 'user_1', 'hash', 1, 1, 0, ?, ?);
	`, models.UTCNow(), models.UTCNow())

	_ = assetRepo.UpsertBinding(ctx, &models.CloudBinding{
		ID:               "binding_1",
		Provider:         "115",
		ProviderUserID:   "u_1",
		Enabled:          1,
		SecretSettingKey: "sec",
	})

	_ = movieRepo.UpsertMovie(ctx, &models.Movie{
		Code:        "SSIS-123",
		Title:       "SSIS-123",
		Category:    "亚洲有码",
		FirstSeenAt: models.UTCNow(),
	})

	// Add Tier 1 permanent magnet
	_ = magnetRepo.UpsertMagnet(ctx, &models.Magnet{
		InfoHash:      "115:binding_1:file_1001",
		MovieCode:     "SSIS-123",
		ResourceKind:  "existing",
		MagnetURL:     "115:binding_1:file_1001",
		Title:         stringPtr("SSIS-123 Permanent"),
		HasChineseSub: 1,
		Enabled:       1,
	})

	fid := "file_1001"
	pick := "pick_1001"
	_ = assetRepo.UpsertAsset(ctx, &models.CloudAsset{
		ID:         "ast_perm_1",
		BindingID:  "binding_1",
		ResourceKey: stringPtr("115:binding_1:file_1001"),
		SourceType: "permanent",
		State:      "ready",
		FileID:     &fid,
		PickCode:   &pick,
	})

	c115Client, _ := client115.NewClient(client115.ClientConfig{BaseURL: mockServer.URL})
	transferManager := NewTransferManager(database, assetRepo, magnetRepo, jobRepo, c115Client, "temp_cid", 7, nil)

	resolver := NewResolver(database, assetRepo, magnetRepo, transferManager, c115Client, 1000, 120, nil)

	// Resolve playback: should resolve to permanent asset and return direct URL
	res, err := resolver.ResolvePlayback(ctx, "SSIS-123", "", "user_1", "device_1")
	if err != nil {
		t.Fatalf("ResolvePlayback failed: %v", err)
	}

	if res.StreamURL != "https://cdn.115.com/download/SSIS-123.mp4?sign=valid" {
		t.Errorf("unexpected stream url: %s", res.StreamURL)
	}
	if !res.IsDirectPlay {
		t.Errorf("expected direct play true")
	}

	// Explicit version selection: resolves specified version
	resExp, err := resolver.ResolvePlayback(ctx, "SSIS-123", "115:binding_1:file_1001", "user_1", "device_1")
	if err != nil || resExp.InfoHash != "115:binding_1:file_1001" {
		t.Fatalf("expected explicit version resolution, got %v", err)
	}

	// Explicit version selection: unknown version must NOT fall back, returns ErrResourceUnavailable
	_, err = resolver.ResolvePlayback(ctx, "SSIS-123", "nonexistent_info_hash", "user_1", "device_1")
	if err != ErrResourceUnavailable {
		t.Errorf("expected ErrResourceUnavailable for unknown explicit version, got %v", err)
	}

	// Cold magnet without ready asset triggers transfer and returns ErrResourcePreparing on wait timeout
	err = movieRepo.UpsertMovie(ctx, &models.Movie{
		Code:        "SSIS-999",
		Title:       "SSIS-999",
		Category:    "亚洲有码",
		FirstSeenAt: models.UTCNow(),
	})
	if err != nil {
		t.Fatalf("failed to insert movie SSIS-999: %v", err)
	}

	err = magnetRepo.UpsertMagnet(ctx, &models.Magnet{
		InfoHash:      "HASH_COLD_NEW",
		MovieCode:     "SSIS-999",
		ResourceKind:  "btih",
		MagnetURL:     "magnet:?xt=urn:btih:HASH_COLD_NEW",
		Title:         stringPtr("SSIS-999 Cold"),
		HasChineseSub: 1,
		Enabled:       1,
	})
	if err != nil {
		t.Fatalf("failed to insert magnet: %v", err)
	}

	fastResolver := NewResolver(database, assetRepo, magnetRepo, transferManager, c115Client, 100, 120, nil)
	_, err = fastResolver.ResolvePlayback(ctx, "SSIS-999", "HASH_COLD_NEW", "user_1", "device_1")
	if err != ErrResourcePreparing {
		t.Errorf("expected ErrResourcePreparing on transfer wait timeout, got %v", err)
	}
}

func stringPtr(s string) *string {
	return &s
}
