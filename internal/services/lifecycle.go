package services

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"mediavault/internal/db"

	"github.com/gin-gonic/gin"
)

// LifecycleManager manages application startup, health probes, and graceful shutdown.
type LifecycleManager struct {
	mu           sync.RWMutex
	database     *db.DB
	serverID     string
	isReady      int32
	readyReason  string
	shutdownCtx  context.Context
	cancelFunc   context.CancelFunc
	workerWg     sync.WaitGroup
	stopClaiming int32
}

// NewLifecycleManager creates a new LifecycleManager.
func NewLifecycleManager(database *db.DB) *LifecycleManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &LifecycleManager{
		database:    database,
		shutdownCtx: ctx,
		cancelFunc:  cancel,
	}
}

// SetReady marks the service ready or not ready with a reason and server_id.
func (lm *LifecycleManager) SetReady(ready bool, serverID string, reason string) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	lm.serverID = serverID
	lm.readyReason = reason
	if ready {
		atomic.StoreInt32(&lm.isReady, 1)
	} else {
		atomic.StoreInt32(&lm.isReady, 0)
	}
}

// IsReady returns whether the service is ready.
func (lm *LifecycleManager) IsReady() bool {
	return atomic.LoadInt32(&lm.isReady) == 1
}

// ShouldStopClaiming returns true when background workers should stop claiming new jobs.
func (lm *LifecycleManager) ShouldStopClaiming() bool {
	return atomic.LoadInt32(&lm.stopClaiming) == 1
}

// Context returns the lifecycle root context, cancelled on shutdown.
func (lm *LifecycleManager) Context() context.Context {
	return lm.shutdownCtx
}

// RegisterWorker adds to the worker WaitGroup.
func (lm *LifecycleManager) RegisterWorker() {
	lm.workerWg.Add(1)
}

// WorkerDone decrements the worker WaitGroup.
func (lm *LifecycleManager) WorkerDone() {
	lm.workerWg.Done()
}

// HandleHealthz responds to /healthz (200 OK if process is running).
func (lm *LifecycleManager) HandleHealthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

// HandleReadyz responds to /readyz (200 OK only if DB is initialized and ready).
func (lm *LifecycleManager) HandleReadyz(c *gin.Context) {
	if !lm.IsReady() {
		lm.mu.RLock()
		reason := lm.readyReason
		lm.mu.RUnlock()

		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "not_ready",
			"reason": reason,
		})
		return
	}

	// Double check DB responsiveness
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	if lm.database != nil {
		if err := lm.database.Reader().PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "not_ready",
				"reason": fmt.Sprintf("database ping failed: %v", err),
			})
			return
		}
	}

	lm.mu.RLock()
	sid := lm.serverID
	lm.mu.RUnlock()

	c.JSON(http.StatusOK, gin.H{
		"status":    "ready",
		"server_id": sid,
	})
}

// Shutdown initiates graceful shutdown:
// 1. Mark not ready and stop claiming new jobs.
// 2. Shut down HTTP server.
// 3. Cancel worker context and wait for in-flight tasks with timeout.
// 4. Close database connections.
func (lm *LifecycleManager) Shutdown(httpSrv *http.Server, timeout time.Duration) error {
	atomic.StoreInt32(&lm.isReady, 0)
	atomic.StoreInt32(&lm.stopClaiming, 1)

	// Cancel worker context
	lm.cancelFunc()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var httpErr error
	if httpSrv != nil {
		httpErr = httpSrv.Shutdown(ctx)
	}

	// Wait for workers in goroutine with timeout
	done := make(chan struct{})
	go func() {
		lm.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}

	if lm.database != nil {
		_ = lm.database.Close()
	}

	return httpErr
}

// CheckDBReadiness verifies DB readiness on startup.
func CheckDBReadiness(ctx context.Context, database *sql.DB) (bool, string, error) {
	if err := database.PingContext(ctx); err != nil {
		return false, "", fmt.Errorf("database unreachable: %w", err)
	}
	info, err := db.ReadSchemaMeta(ctx, database)
	if err != nil {
		return false, "", fmt.Errorf("failed to read schema meta: %w", err)
	}
	if info.Version != db.CurrentSchemaVersion {
		return false, "", fmt.Errorf("schema version mismatch: got %d, expected %d", info.Version, db.CurrentSchemaVersion)
	}
	return true, info.ServerID, nil
}
