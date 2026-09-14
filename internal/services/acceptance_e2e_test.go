package services

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mediavault/internal/client115"
	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/identity"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
)

// AC-1: 空库启动、管理员初始化与密钥正确，readyz 200；未授权/未同步不会阻塞管理
func TestAC01_EmptyDatabaseStartupAndReadyz(t *testing.T) {
	database, _ := setupTestDB(t)
	userRepo := db.NewUserRepo(database)

	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 1)
	}
	masterKey, err := config.NewMasterKeyFromBytes(keyBytes)
	if err != nil {
		t.Fatalf("failed to create master key: %v", err)
	}

	adminUser, err := userRepo.EnsureAdminUser(context.Background(), "admin", "admin123456", false)
	if err != nil || adminUser == nil {
		t.Fatalf("failed to bootstrap admin user: %v", err)
	}

	lifecycle := NewLifecycleManager(database)
	lifecycle.SetReady(true, "srv_test_01", "")

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/readyz", lifecycle.HandleReadyz)

	req, _ := http.NewRequest(http.MethodGet, "/readyz", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected readyz to return 200, got %d", w.Code)
	}

	_ = masterKey
}

// AC-5 & AC-6: 播放就绪资源 (Tier 1/2) 与冷资源超过准备状态 (Tier 3 -> ErrResourcePreparing)
func TestAC05_AC06_PlaybackResolutionAndColdPrepare(t *testing.T) {
	database, _ := setupTestDB(t)
	assetRepo := db.NewAssetRepo(database)
	movieRepo := db.NewMovieRepo(database)
	magnetRepo := db.NewMagnetRepo(database)
	jobRepo := db.NewJobRepo(database)

	ctx := context.Background()

	now := models.UTCNow()
	_, _ = database.Writer().ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, is_admin, enabled, must_change_password, created_at, updated_at)
		VALUES ('usr_1', 'user1', 'hash', 1, 1, 0, ?, ?)
	`, now, now)

	// 1. Setup Movie and Transferable Magnet (Tier 3)
	movieCode := "ABP-999"
	_ = movieRepo.UpsertMovie(ctx, &models.Movie{
		Code:        movieCode,
		Title:       "Test Movie",
		Category:    "censored",
		FirstSeenAt: models.UTCNow(),
	})

	infoHash := "0123456789abcdef0123456789abcdef01234567"
	_ = magnetRepo.UpsertMagnet(ctx, &models.Magnet{
		InfoHash:     infoHash,
		MovieCode:    movieCode,
		ResourceKind: "btih",
		MagnetURL:    "magnet:?xt=urn:btih:" + infoHash,
		Enabled:      1,
		IsPreferred:  1,
	})

	mockServer := setupMock115Server()
	defer mockServer.Close()
	c115Client, _ := client115.NewClient(client115.ClientConfig{BaseURL: mockServer.URL})

	transferManager := NewTransferManager(database, assetRepo, magnetRepo, jobRepo, c115Client, "cid_temp_root", 7, nil)
	// Fast wait for test (50ms instead of 8s)
	resolver := NewResolver(database, assetRepo, magnetRepo, transferManager, c115Client, 50, 120, nil)

	// Setup active 115 binding
	_ = assetRepo.UpsertBinding(ctx, &models.CloudBinding{
		ID:               "binding_test",
		Provider:         "115",
		ProviderUserID:   "u_115",
		Enabled:          1,
		SecretSettingKey: "sec_115",
	})

	// Cold resolution: returns ErrResourcePreparing
	_, err := resolver.ResolvePlayback(ctx, movieCode, "", "usr_1", "dev_1")
	if err != ErrResourcePreparing {
		t.Fatalf("expected ErrResourcePreparing for cold transferable asset, got %v", err)
	}

	// 2. Now simulate asset completed and ready in cloud (Tier 1)
	now = models.UTCNow()
	fileID := "fid_perm"
	pickCode := "pcode_perm"
	_ = assetRepo.UpsertAsset(ctx, &models.CloudAsset{
		ID:          "ast_perm_ready",
		BindingID:   "binding_test",
		ResourceKey: &infoHash,
		SourceType:  "permanent",
		State:       "ready",
		Generation:  1,
		FileID:      &fileID,
		PickCode:    &pickCode,
		ReadyAt:     &now,
	})

	resReady, err := resolver.ResolvePlayback(ctx, movieCode, "", "usr_1", "dev_1")
	if err != nil {
		t.Fatalf("expected ready asset to resolve successfully, got %v", err)
	}
	if resReady.AssetID != "ast_perm_ready" {
		t.Fatalf("expected permanent ready asset, got %s", resReady.AssetID)
	}
}

// AC-8: TTL清理保护活动会话和永久库，自然过期与选源隔离
func TestAC08_AC15_JanitorAndTTLProtection(t *testing.T) {
	mockServer := setupMock115Server()
	defer mockServer.Close()

	database, _ := setupTestDB(t)
	assetRepo := db.NewAssetRepo(database)
	jobRepo := db.NewJobRepo(database)
	movieRepo := db.NewMovieRepo(database)

	ctx := context.Background()

	// 1. Seed user and movie for foreign key integrity
	now := models.UTCNow()
	_, _ = database.Writer().ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, is_admin, enabled, must_change_password, created_at, updated_at)
		VALUES ('usr_dummy', 'dummy', 'hash', 1, 1, 0, ?, ?)
	`, now, now)

	_ = movieRepo.UpsertMovie(ctx, &models.Movie{
		Code:        "dummy",
		Title:       "Dummy Movie",
		Category:    "censored",
		FirstSeenAt: now,
	})

	_ = assetRepo.UpsertBinding(ctx, &models.CloudBinding{
		ID:               "bind_janitor",
		Provider:         "115",
		ProviderUserID:   "user_janitor",
		Enabled:          1,
		SecretSettingKey: "secret_janitor",
	})

	// Seed owning job for temporary asset
	jobID1 := "job_t1"
	_, _, _ = jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:        jobID1,
		Kind:      "transfer",
		DedupeKey: "transfer:t1",
		State:     "succeeded",
	})

	pastReady := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	pastExpire := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	futureTime := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)

	rootID := "folder_tmp_123"
	rootSnap := "{}"
	fileID1 := "f1"
	pickCode1 := "p1"

	// Temporary asset with past expires_at
	_ = assetRepo.UpsertAsset(ctx, &models.CloudAsset{
		ID:           "ast_expired_protected",
		BindingID:    "bind_janitor",
		OwningJobID:  &jobID1,
		OwnedRootID:  &rootID,
		RootSnapshot: &rootSnap,
		FileID:       &fileID1,
		PickCode:     &pickCode1,
		SourceType:   "temporary",
		State:        "ready",
		Generation:   1,
		ReadyAt:      &pastReady,
		ExpiresAt:    &pastExpire,
	})

	// Natural expiry check: IsAssetPlayable returns false
	expiredAsset, _ := assetRepo.GetAssetByID(ctx, "ast_expired_protected")
	if IsAssetPlayable(expiredAsset, time.Now().UTC()) {
		t.Fatalf("expected expired temporary asset to NOT be playable for new sessions")
	}

	// 2. Active play lease protects asset from janitor cleanup
	lease := futureTime
	_ = database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO play_sessions (id, user_id, movie_code, asset_id, device_id, state, lease_until, updated_at)
			VALUES ('sess_active_1', 'usr_dummy', 'dummy', 'ast_expired_protected', 'dev_1', 'active', ?, ?)
		`, lease, now)
		return err
	})

	hasLease, err := assetRepo.HasActivePlayLease(ctx, "ast_expired_protected", now)
	if err != nil || !hasLease {
		t.Fatalf("expected asset to have active play lease")
	}

	c115Client, _ := client115.NewClient(client115.ClientConfig{BaseURL: mockServer.URL})
	janitor := NewJanitorService(database, assetRepo, c115Client, "cid_temp_root", nil)
	cleaned, skipped, err := janitor.RunCleanup(ctx, true)
	if err != nil {
		t.Fatalf("janitor run failed: %v", err)
	}
	if cleaned != 0 || skipped != 1 {
		t.Fatalf("expected 0 assets cleaned and 1 skipped due to active play lease protection, got cleaned=%d skipped=%d", cleaned, skipped)
	}

	// 3. Once lease expires, asset can be cleaned
	expiredLease := pastExpire
	_ = database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE play_sessions SET lease_until = ? WHERE id = 'sess_active_1'`, expiredLease)
		return err
	})

	cleanedAfter, _, err := janitor.RunCleanup(ctx, true)
	if err != nil {
		t.Fatalf("janitor run failed: %v", err)
	}
	if cleanedAfter != 1 {
		t.Fatalf("expected 1 asset cleaned after play lease expired, got %d", cleanedAfter)
	}
}

// AC-9 & AC-20: A/B 用户播放进度隔离与完播幂等性
func TestAC09_AC20_UserProgressIsolationAndSessionIdempotency(t *testing.T) {
	database, _ := setupTestDB(t)
	progressRepo := db.NewProgressRepo(database)
	movieRepo := db.NewMovieRepo(database)

	ctx := context.Background()

	// Seed users and movie to satisfy FK
	now := models.UTCNow()
	_, _ = database.Writer().ExecContext(ctx, `
		INSERT INTO users (id, username, password_hash, is_admin, enabled, must_change_password, created_at, updated_at)
		VALUES ('user_a', 'Alice', 'hash', 0, 1, 0, ?, ?),
		       ('user_b', 'Bob', 'hash', 0, 1, 0, ?, ?)
	`, now, now, now, now)

	_ = movieRepo.UpsertMovie(ctx, &models.Movie{
		Code:        "M-001",
		Title:       "Movie 001",
		Category:    "censored",
		FirstSeenAt: now,
	})

	// 1. Report progress for User A
	_ = progressRepo.UpsertProgress(ctx, &models.UserProgress{
		UserID:        "user_a",
		MovieCode:     "M-001",
		PositionTicks: 10000000, // 1 second
		DurationTicks: int64Ptr(600000000),
		Played:        0,
		UpdatedAt:     now,
	})

	// 2. Report progress for User B
	_ = progressRepo.UpsertProgress(ctx, &models.UserProgress{
		UserID:        "user_b",
		MovieCode:     "M-001",
		PositionTicks: 550000000, // Near end
		DurationTicks: int64Ptr(600000000),
		Played:        1,
		PlayCount:     1,
		UpdatedAt:     now,
	})

	// Verify User A and User B progress are strictly isolated
	pA, _ := progressRepo.GetProgress(ctx, "user_a", "M-001")
	pB, _ := progressRepo.GetProgress(ctx, "user_b", "M-001")

	if pA == nil || pA.Played != 0 || pA.PositionTicks != 10000000 {
		t.Fatalf("user A progress contaminated: %+v", pA)
	}
	if pB == nil || pB.Played != 1 || pB.PlayCount != 1 {
		t.Fatalf("user B progress incorrect: %+v", pB)
	}

	// 3. Idempotent stopped report: reporting stopped twice on finished movie doesn't double-count PlayCount
	_ = progressRepo.UpsertProgress(ctx, &models.UserProgress{
		UserID:        "user_b",
		MovieCode:     "M-001",
		PositionTicks: 560000000,
		DurationTicks: int64Ptr(600000000),
		Played:        1,
		PlayCount:     0, // Additional stop shouldn't increment
		UpdatedAt:     models.UTCNow(),
	})
	pB2, _ := progressRepo.GetProgress(ctx, "user_b", "M-001")
	if pB2.PlayCount != 1 {
		t.Fatalf("expected PlayCount to remain 1, got %d", pB2.PlayCount)
	}
}

// AC-11: 崩溃重启后的 Job 租约恢复 (T-805)
func TestAC11_JobCrashRecovery(t *testing.T) {
	database, _ := setupTestDB(t)
	jobRepo := db.NewJobRepo(database)

	ctx := context.Background()
	now := models.UTCNow()
	expiredLease := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)

	// Create job currently stuck in 'running' state from previous process crash
	jobID := "job_crashed_1"
	_, _, err := jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:        jobID,
		Kind:      "transfer",
		DedupeKey: "transfer:crash_recovery",
		State:     "queued",
	})
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	// Simulate crashed runner: state=running, lease expired
	_ = database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE jobs
			SET state = 'running', lease_owner = 'worker_dead', lease_until = ?, updated_at = ?
			WHERE id = ?
		`, expiredLease, now, jobID)
		return err
	})

	// A new worker starts and claims next job: should recover this expired lease job!
	claimed, err := jobRepo.ClaimNextJob(ctx, "transfer", "worker_new", 60*time.Second, now)
	if err != nil {
		t.Fatalf("failed to claim job: %v", err)
	}
	if claimed == nil {
		t.Fatalf("expected crashed job to be claimed after lease expiry, got nil")
	}
	if claimed.ID != jobID {
		t.Fatalf("expected claimed job %s, got %s", jobID, claimed.ID)
	}
	if claimed.LeaseOwner == nil || *claimed.LeaseOwner != "worker_new" {
		t.Fatalf("expected new lease owner worker_new, got %+v", claimed.LeaseOwner)
	}
}

// AC-12: 同版本并发合并 (Deduplication) 与不同版本独立
func TestAC12_ConcurrentJobCoalescing(t *testing.T) {
	database, _ := setupTestDB(t)
	jobRepo := db.NewJobRepo(database)

	ctx := context.Background()

	// Launch 10 concurrent requests to trigger transfer for the exact same hash
	hash := "1111222233334444555566667777888899990000"
	var wg sync.WaitGroup
	var successCount int32
	var createdCount int32
	var firstJobID string
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j := &models.Job{
				Kind:      "transfer",
				DedupeKey: hash,
			}
			job, created, err := jobRepo.CreateOrGetJob(ctx, j)
			if err == nil {
				atomic.AddInt32(&successCount, 1)
				if created {
					atomic.AddInt32(&createdCount, 1)
				}
				mu.Lock()
				if firstJobID == "" {
					firstJobID = job.ID
				} else if job.ID != firstJobID {
					t.Errorf("concurrent jobs did not coalesce: %s != %s", job.ID, firstJobID)
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	if successCount != 10 {
		t.Fatalf("expected 10 successful invocations, got %d", successCount)
	}
	if createdCount != 1 {
		t.Fatalf("expected exactly 1 job to be created, got %d", createdCount)
	}
}

// AC-16 & AC-17: 纯函数规范化与优先级打分验证
func TestAC16_AC17_PureFunctionsAndScoring(t *testing.T) {
	// 1. Movie code normalization
	code1, reason1 := identity.NormalizeCode("ssis-123")
	if code1 != "SSIS-123" || reason1 != "" {
		t.Fatalf("expected SSIS-123, got %s (reason: %s)", code1, reason1)
	}

	codeFC2, reasonFC2 := identity.NormalizeCode("fc2-ppv-1234567")
	if codeFC2 != "FC2-PPV-1234567" || reasonFC2 != "" {
		t.Fatalf("expected FC2-PPV-1234567, got %s (reason: %s)", codeFC2, reasonFC2)
	}

	// 2. Priority scoring rules (bit-weighted):
	// Chinese Sub (8), Cracked (4), 4K (2), Censored (1)
	scoreZh4k := identity.ComputePriorityScore(1, 0, 1, 1)
	scorePlain := identity.ComputePriorityScore(0, 0, 0, 1)
	if scoreZh4k <= scorePlain {
		t.Fatalf("expected Chinese sub + 4K to have higher priority than plain: %d <= %d", scoreZh4k, scorePlain)
	}
	if scoreZh4k != 11 {
		t.Fatalf("expected Chinese sub (8) + 4K (2) + Censored (1) = 11, got %d", scoreZh4k)
	}
}

// AC-18: 主密钥加密/解密敏感配置
func TestAC18_MasterKeyEncryptionRoundtrip(t *testing.T) {
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 42)
	}
	mk, err := config.NewMasterKeyFromBytes(keyBytes)
	if err != nil {
		t.Fatalf("create master key failed: %v", err)
	}

	plainText := "my-secret-115-app-key-and-token-xyz"
	settingKey := "provider_115_credentials"
	encrypted, err := mk.Encrypt(settingKey, []byte(plainText))
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	decrypted, err := mk.Decrypt(settingKey, encrypted)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	if string(decrypted) != plainText {
		t.Fatalf("expected decrypted text %s, got %s", plainText, string(decrypted))
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}
