package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"mediavault/internal/models"
)

func setupTestDB(t *testing.T) (*DB, context.Context) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test_repo.db")

	database, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})

	ctx := context.Background()
	if _, err := InitEmptyDatabase(ctx, database); err != nil {
		t.Fatalf("InitEmptyDatabase failed: %v", err)
	}

	return database, ctx
}

func TestPreferredUniquenessAndCascade(t *testing.T) {
	database, ctx := setupTestDB(t)

	movieRepo := NewMovieRepo(database)
	magnetRepo := NewMagnetRepo(database)

	// 1. Insert movie
	m := &models.Movie{
		Code:        "TEST-001",
		Category:    "有码",
		FirstSeenAt: models.UTCNow(),
		Title:       "Test Movie",
	}
	if err := movieRepo.UpsertMovie(ctx, m); err != nil {
		t.Fatalf("UpsertMovie failed: %v", err)
	}

	// 2. Insert two magnets
	mag1 := &models.Magnet{
		InfoHash:      "HASHAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		MovieCode:     "TEST-001",
		ResourceKind:  "btih",
		MagnetURL:     "magnet:?xt=urn:btih:HASHAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		HasChineseSub: 1,
		Enabled:       1,
	}
	mag2 := &models.Magnet{
		InfoHash:      "HASHBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		MovieCode:     "TEST-001",
		ResourceKind:  "btih",
		MagnetURL:     "magnet:?xt=urn:btih:HASHBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		HasChineseSub: 0,
		Enabled:       1,
	}
	if err := magnetRepo.UpsertMagnet(ctx, mag1); err != nil {
		t.Fatalf("UpsertMagnet 1 failed: %v", err)
	}
	if err := magnetRepo.UpsertMagnet(ctx, mag2); err != nil {
		t.Fatalf("UpsertMagnet 2 failed: %v", err)
	}

	// 3. Set mag1 as preferred
	if err := magnetRepo.SetPreferredMagnet(ctx, "TEST-001", mag1.InfoHash); err != nil {
		t.Fatalf("SetPreferredMagnet mag1 failed: %v", err)
	}

	// 4. Set mag2 as preferred -> mag1 should automatically be cleared
	if err := magnetRepo.SetPreferredMagnet(ctx, "TEST-001", mag2.InfoHash); err != nil {
		t.Fatalf("SetPreferredMagnet mag2 failed: %v", err)
	}

	mags, err := magnetRepo.ListMagnetsByMovie(ctx, "TEST-001")
	if err != nil || len(mags) != 2 {
		t.Fatalf("ListMagnetsByMovie failed: %v", err)
	}

	var preferredCount int
	for _, mag := range mags {
		if mag.IsPreferred == 1 {
			preferredCount++
			if mag.InfoHash != mag2.InfoHash {
				t.Errorf("expected mag2 to be preferred, got %s", mag.InfoHash)
			}
		}
	}
	if preferredCount != 1 {
		t.Errorf("expected exactly 1 preferred magnet, got %d", preferredCount)
	}

	// 5. Delete movie -> cascades to magnets
	if err := database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM offline_movies WHERE code = 'TEST-001'")
		return err
	}); err != nil {
		t.Fatalf("delete movie failed: %v", err)
	}
	magsAfter, _ := magnetRepo.ListMagnetsByMovie(ctx, "TEST-001")
	if len(magsAfter) != 0 {
		t.Errorf("expected 0 magnets after cascade delete, got %d", len(magsAfter))
	}
}

func TestResolveDefaultSourceTiers(t *testing.T) {
	database, ctx := setupTestDB(t)

	movieRepo := NewMovieRepo(database)
	magnetRepo := NewMagnetRepo(database)

	movieCode := "TIER-001"
	if err := movieRepo.UpsertMovie(ctx, &models.Movie{
		Code:        movieCode,
		Category:    "有码",
		FirstSeenAt: models.UTCNow(),
	}); err != nil {
		t.Fatalf("upsert movie: %v", err)
	}

	// Add a binding
	bindingID := "bind_115_test"
	now := models.UTCNow()

	// Direct query execution on writer for setup
	_, err := database.writer.ExecContext(ctx, `
		INSERT INTO cloud_bindings (id, provider, provider_user_id, enabled, secret_setting_key, created_at, updated_at)
		VALUES (?, '115', 'u123', 1, 'sec_test', ?, ?)
	`, bindingID, now, now)
	if err != nil {
		t.Fatalf("insert cloud_binding: %v", err)
	}

	mag1 := &models.Magnet{
		InfoHash:      "HASH_TIER1_PERM",
		MovieCode:     movieCode,
		ResourceKind:  "btih",
		MagnetURL:     "magnet:?xt=urn:btih:HASH_TIER1_PERM",
		HasChineseSub: 0,
		Enabled:       1,
	}
	mag2 := &models.Magnet{
		InfoHash:      "HASH_TIER2_TEMP",
		MovieCode:     movieCode,
		ResourceKind:  "btih",
		MagnetURL:     "magnet:?xt=urn:btih:HASH_TIER2_TEMP",
		HasChineseSub: 1, // Higher priority score, but temporary
		Enabled:       1,
	}
	_ = magnetRepo.UpsertMagnet(ctx, mag1)
	_ = magnetRepo.UpsertMagnet(ctx, mag2)

	// Add permanent ready asset for mag1
	_, err = database.writer.ExecContext(ctx, `
		INSERT INTO cloud_assets (id, binding_id, resource_key, source_type, state, file_id, pick_code, created_at, updated_at)
		VALUES ('asset_perm', ?, ?, 'permanent', 'ready', 'fid_perm', 'pick_perm', ?, ?)
	`, bindingID, mag1.InfoHash, now, now)
	if err != nil {
		t.Fatalf("insert permanent asset: %v", err)
	}

	// Insert a job for owning_job_id
	_, err = database.writer.ExecContext(ctx, `
		INSERT INTO jobs (id, kind, dedupe_key, state, created_at, updated_at)
		VALUES ('job_temp_owner', 'transfer', 'test_dedupe_temp', 'succeeded', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatalf("insert job_temp_owner: %v", err)
	}

	// Add temporary ready asset for mag2 (expires in 1 hour)
	futureExpire := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	readyAt := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	_, err = database.writer.ExecContext(ctx, `
		INSERT INTO cloud_assets (id, binding_id, resource_key, owning_job_id, source_type, state, file_id, pick_code, ready_at, expires_at, owned_root_id, root_snapshot, created_at, updated_at)
		VALUES ('asset_temp', ?, ?, 'job_temp_owner', 'temporary', 'ready', 'fid_temp', 'pick_temp', ?, ?, 'root_1', 'snap', ?, ?)
	`, bindingID, mag2.InfoHash, readyAt, futureExpire, now, now)
	if err != nil {
		t.Fatalf("insert temporary asset: %v", err)
	}

	// Resolve: Tier 1 (permanent) must take precedence over Tier 2 (temporary)
	resolved, err := magnetRepo.ResolveDefaultSource(ctx, movieCode, now)
	if err != nil {
		t.Fatalf("ResolveDefaultSource failed: %v", err)
	}
	if resolved == nil {
		t.Fatalf("expected resolved source, got nil")
	}
	if resolved.Tier != 1 {
		t.Errorf("expected Tier 1, got Tier %d", resolved.Tier)
	}
	if resolved.Magnet.InfoHash != mag1.InfoHash {
		t.Errorf("expected mag1 to resolve, got %s", resolved.Magnet.InfoHash)
	}
}

func TestJobDeduplicationAndLeasing(t *testing.T) {
	database, ctx := setupTestDB(t)
	jobRepo := NewJobRepo(database)

	now := models.UTCNow()

	// 1. Create Job
	j1 := &models.Job{
		Kind:      "transfer",
		DedupeKey: "bind115_hash123",
		State:     "queued",
	}
	createdJob, wasCreated, err := jobRepo.CreateOrGetJob(ctx, j1)
	if err != nil {
		t.Fatalf("CreateOrGetJob 1 failed: %v", err)
	}
	if !wasCreated {
		t.Errorf("expected job was created")
	}

	// 2. Try creating duplicate active job with same (kind, dedupe_key)
	j2 := &models.Job{
		Kind:      "transfer",
		DedupeKey: "bind115_hash123",
		State:     "queued",
	}
	dupJob, wasCreated2, err := jobRepo.CreateOrGetJob(ctx, j2)
	if err != nil {
		t.Fatalf("CreateOrGetJob 2 failed: %v", err)
	}
	if wasCreated2 {
		t.Errorf("expected duplicate job NOT created")
	}
	if dupJob.ID != createdJob.ID {
		t.Errorf("expected duplicate job to return existing ID %s, got %s", createdJob.ID, dupJob.ID)
	}

	// 3. Claim job with lease
	claimed, err := jobRepo.ClaimNextJob(ctx, "transfer", "worker_1", 2*time.Minute, now)
	if err != nil {
		t.Fatalf("ClaimNextJob failed: %v", err)
	}
	if claimed == nil || claimed.ID != createdJob.ID {
		t.Fatalf("expected to claim job %s, got %v", createdJob.ID, claimed)
	}
	if claimed.State != "running" || *claimed.LeaseOwner != "worker_1" {
		t.Errorf("expected state running with leaseOwner worker_1, got state=%s, owner=%v", claimed.State, claimed.LeaseOwner)
	}

	// 4. Finish job
	if err := jobRepo.FinishJob(ctx, claimed.ID, "succeeded", `{"status":"ok"}`, nil); err != nil {
		t.Fatalf("FinishJob failed: %v", err)
	}

	finished, err := jobRepo.GetJobByID(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("GetJobByID failed: %v", err)
	}
	if finished.State != "succeeded" {
		t.Errorf("expected state succeeded, got %s", finished.State)
	}
}

func TestUserAndAuthSessions(t *testing.T) {
	database, ctx := setupTestDB(t)
	userRepo := NewUserRepo(database)

	// 1. Ensure admin user
	admin, err := userRepo.EnsureAdminUser(ctx, "admin", "Admin123456", true)
	if err != nil {
		t.Fatalf("EnsureAdminUser failed: %v", err)
	}
	if admin.MustChangePassword != 1 {
		t.Errorf("expected must_change_password=1")
	}

	// Verify password
	ok, err := VerifyPassword(admin.PasswordHash, "Admin123456")
	if err != nil || !ok {
		t.Fatalf("VerifyPassword failed: ok=%v, err=%v", ok, err)
	}

	// 2. Change password
	if err := userRepo.UpdatePassword(ctx, admin.ID, "NewSecretPassword"); err != nil {
		t.Fatalf("UpdatePassword failed: %v", err)
	}
	adminUpdated, _ := userRepo.GetUserByID(ctx, admin.ID)
	if adminUpdated.MustChangePassword != 0 {
		t.Errorf("expected must_change_password=0 after password update")
	}

	// 3. Create Session
	tokenHash := "token_hash_admin_123"
	now := models.UTCNow()
	expire := time.Now().UTC().Add(12 * time.Hour).Format(time.RFC3339)
	session := &models.AuthSession{
		ID:        "sess_001",
		UserID:    admin.ID,
		TokenHash: tokenHash,
		Audience:  "admin",
		DeviceID:  "dev_01",
		CreatedAt: now,
		ExpiresAt: expire,
	}
	if err := userRepo.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// 4. Authenticate session
	foundSess, foundUser, err := userRepo.GetSessionByTokenHash(ctx, tokenHash, "admin", now)
	if err != nil || foundSess == nil || foundUser == nil {
		t.Fatalf("GetSessionByTokenHash failed: %v", err)
	}

	// 5. Audience mismatch check: admin session token cannot be used for emby audience
	_, _, err = userRepo.GetSessionByTokenHash(ctx, tokenHash, "emby", now)
	if err == nil {
		t.Fatalf("expected audience mismatch error, got nil")
	}
}
