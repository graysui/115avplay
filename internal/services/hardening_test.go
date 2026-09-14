package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/models"
)



// T-802 & T-803: Test Persistent Alert Engine & Webhook
func TestAlertEngineAndWebhook(t *testing.T) {
	database, _ := setupTestDB(t)
	settingsRepo := db.NewSettingsRepo(database)

	var webhookHits int32
	var lastReceivedKey string
	var lastReceivedState string

	// Create test webhook receiver
	webhookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&webhookHits, 1)
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		lastReceivedKey, _ = payload["key"].(string)
		lastReceivedState, _ = payload["state"].(string)
		w.WriteHeader(http.StatusOK)
	}))
	defer webhookSrv.Close()

	keyBytes := make([]byte, 32)
	masterKey, _ := config.NewMasterKeyFromBytes(keyBytes)
	appConfig := &config.AppConfig{
		MasterKey: masterKey,
	}

	// Encrypt webhook url into settings
	envJSON, _ := masterKey.Encrypt("alert_webhook", []byte(webhookSrv.URL))
	_, err := settingsRepo.UpdateSettings(context.Background(), 0, nil, map[string]string{
		"alert_webhook": envJSON,
	}, nil)
	if err != nil {
		t.Fatalf("failed to update settings: %v", err)
	}

	engine := NewAlertEngine(settingsRepo, appConfig, nil)

	// 1. Trigger Alert
	ctx := context.Background()
	err = engine.TriggerAlert(ctx, "transfer_unknown:job_123", "Transfer job entered reconcile state")
	if err != nil {
		t.Fatalf("expected trigger alert to succeed: %v", err)
	}

	// Wait for async webhook delivery
	time.Sleep(300 * time.Millisecond)

	if atomic.LoadInt32(&webhookHits) == 0 {
		t.Fatalf("expected webhook to be called")
	}
	if lastReceivedKey != "transfer_unknown:job_123" || lastReceivedState != "active" {
		t.Fatalf("unexpected webhook payload: key=%s, state=%s", lastReceivedKey, lastReceivedState)
	}

	// Verify alert is in DB
	alerts, err := settingsRepo.ListAlerts(ctx, true)
	if err != nil || len(alerts) == 0 {
		t.Fatalf("expected active alert in DB")
	}
	if alerts[0].State != "active" {
		t.Fatalf("expected alert state active, got %s", alerts[0].State)
	}

	// 2. Resolve Alert
	err = engine.ResolveAlert(ctx, "transfer_unknown:job_123")
	if err != nil {
		t.Fatalf("expected resolve alert to succeed: %v", err)
	}

	time.Sleep(300 * time.Millisecond)
	if lastReceivedState != "resolved" {
		t.Fatalf("expected resolved webhook delivery, got %s", lastReceivedState)
	}

	// Verify alert in DB is now resolved
	activeAlerts, _ := settingsRepo.ListAlerts(ctx, true)
	if len(activeAlerts) != 0 {
		t.Fatalf("expected 0 active alerts after resolve, got %d", len(activeAlerts))
	}
}

// T-804: Test Storage Retention (Image Cache Prune, Log Prune, Job Protection)
func TestStorageRetention(t *testing.T) {
	database, tmpDir := setupTestDB(t)
	settingsRepo := db.NewSettingsRepo(database)
	alertEngine := NewAlertEngine(settingsRepo, nil, nil)
	retention := NewRetentionManager(database, settingsRepo, alertEngine, tmpDir, nil)

	ctx := context.Background()

	// 1. Test Disk Space Check
	ok, free, min, err := retention.CheckDiskSpace(ctx)
	if err != nil {
		t.Fatalf("expected disk space check to run without error: %v", err)
	}
	if !ok && free >= min {
		t.Fatalf("expected ok=true when free >= min")
	}

	// 2. Test Image Cache Prune
	cacheDir := filepath.Join(tmpDir, "cache", "images")
	_ = os.MkdirAll(cacheDir, 0755)

	// Create dummy files (total 3 * 10KB = 30KB)
	f1 := filepath.Join(cacheDir, "img1.jpg")
	f2 := filepath.Join(cacheDir, "img2.jpg")
	f3 := filepath.Join(cacheDir, "img3.jpg")
	dummyData := make([]byte, 10240)
	_ = os.WriteFile(f1, dummyData, 0644)
	time.Sleep(20 * time.Millisecond)
	_ = os.WriteFile(f2, dummyData, 0644)
	time.Sleep(20 * time.Millisecond)
	_ = os.WriteFile(f3, dummyData, 0644)

	// Set image_cache_bytes to 20KB (so total 30KB triggers prune down to 80% of 20KB = 16KB)
	_, _ = settingsRepo.UpdateSettings(ctx, 0, map[string]string{
		"image_cache_bytes": "20480",
	}, nil, nil)

	freed, err := retention.PruneImageCache(ctx)
	if err != nil {
		t.Fatalf("expected image cache prune to succeed: %v", err)
	}
	if freed == 0 {
		t.Fatalf("expected some bytes to be freed, got %d", freed)
	}
	// Oldest file (f1) should have been pruned
	if _, err := os.Stat(f1); !os.IsNotExist(err) {
		t.Fatalf("expected oldest file f1 to be pruned")
	}

	// 3. Test Job Retention and Reference Protection
	jobRepo := db.NewJobRepo(database)
	assetRepo := db.NewAssetRepo(database)

	oldTime := time.Now().UTC().AddDate(0, 0, -40).Format(time.RFC3339)

	// Job 1: Succeeded, old, unreferenced -> should be pruned
	job1, _, err := jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:        "job_unreferenced_old",
		Kind:      "transfer",
		DedupeKey: "k1",
	})
	if err != nil {
		t.Fatalf("failed to create job1: %v", err)
	}
	_ = jobRepo.FinishJob(ctx, job1.ID, "succeeded", "{}", nil)
	_ = database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET completed_at = ? WHERE id = ?`, oldTime, job1.ID)
		return err
	})

	// Job 2: Succeeded, old, BUT referenced by cloud_assets -> PROTECTED from deletion!
	job2, _, err := jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:        "job_referenced_old",
		Kind:      "transfer",
		DedupeKey: "k2",
	})
	if err != nil {
		t.Fatalf("failed to create job2: %v", err)
	}
	_ = jobRepo.FinishJob(ctx, job2.ID, "succeeded", "{}", nil)
	_ = database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET completed_at = ? WHERE id = ?`, oldTime, job2.ID)
		return err
	})
	// Insert valid binding
	_ = assetRepo.UpsertBinding(ctx, &models.CloudBinding{
		ID:               "bind_test",
		Provider:         "115",
		ProviderUserID:   "user_115",
		Enabled:          1,
		SecretSettingKey: "secret_115",
	})

	// Reference job2 in cloud_assets
	now := models.UTCNow()
	exp := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)
	rootID := "root_temp"
	rootSnap := "{}"
	fileID := "fid_123"
	pickCode := "pcode_123"
	err = assetRepo.UpsertAsset(ctx, &models.CloudAsset{
		ID:           "ast_ref_test",
		BindingID:    "bind_test",
		OwningJobID:  &job2.ID,
		OwnedRootID:  &rootID,
		RootSnapshot: &rootSnap,
		FileID:       &fileID,
		PickCode:     &pickCode,
		SourceType:   "temporary",
		State:        "ready",
		Generation:   1,
		ReadyAt:      &now,
		ExpiresAt:    &exp,
	})
	if err != nil {
		t.Fatalf("failed to insert asset referencing job2: %v", err)
	}

	// Run job pruning (default 30 days retention)
	deletedJobs, err := retention.PruneCompletedJobs(ctx)
	if err != nil {
		t.Fatalf("expected prune completed jobs to succeed: %v", err)
	}
	if deletedJobs < 1 {
		t.Fatalf("expected at least 1 job to be deleted, got %d", deletedJobs)
	}

	// Verify job1 is deleted
	j1, _ := jobRepo.GetJobByID(ctx, job1.ID)
	if j1 != nil {
		t.Fatalf("expected unreferenced old job1 to be deleted")
	}

	// Verify job2 is PRESERVED
	j2, _ := jobRepo.GetJobByID(ctx, job2.ID)
	if j2 == nil {
		t.Fatalf("expected referenced old job2 to be preserved by reference protection constraint!")
	}
}

// T-805: Fault injection and lease recovery test
func TestLeaseRecoveryAfterCrash(t *testing.T) {
	database, _ := setupTestDB(t)
	jobRepo := db.NewJobRepo(database)

	ctx := context.Background()

	// Simulate a job that was running when process abruptly died (lease expired)
	job, _, _ := jobRepo.CreateOrGetJob(ctx, &models.Job{
		ID:        "job_crashed_worker",
		Kind:      "transfer",
		DedupeKey: "crashed_1",
	})

	// Worker 1 claims job
	claimed, err := jobRepo.ClaimJobByID(ctx, job.ID, "worker_dead", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("failed to claim job: %v", err)
	}
	if claimed.State != "running" {
		t.Fatalf("expected running state")
	}

	// Worker 1 dies, lease expires
	pastTime := time.Now().UTC().Add(-5 * time.Second).Format(time.RFC3339)
	_ = database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE jobs SET lease_until = ? WHERE id = ?`, pastTime, job.ID)
		return err
	})

	// Worker 2 (rebooted service) should be able to claim the expired job
	reclaimed, err := jobRepo.ClaimNextJob(ctx, "transfer", "worker_alive", 5*time.Second, models.UTCNow())
	if err != nil {
		t.Fatalf("expected new worker to claim expired job: %v", err)
	}
	if reclaimed == nil {
		t.Fatalf("expected reclaimed job to be non-nil")
	}
	if reclaimed.ID != job.ID {
		t.Fatalf("expected reclaimed job ID %s, got %s", job.ID, reclaimed.ID)
	}
	if reclaimed.Generation <= claimed.Generation {
		t.Fatalf("expected generation to increment on reclaim: %d <= %d", reclaimed.Generation, claimed.Generation)
	}
}
