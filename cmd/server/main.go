package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mediavault/internal/api/middleware"
	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/logger"
	"mediavault/internal/services"
	"mediavault/web"

	"github.com/gin-gonic/gin"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "MediaVault fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Load config from environment and defaults
	cfg, err := config.LoadFromEnv()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// 2. Initialize logger with ring buffer and optional file logging
	logDir := filepath.Join(cfg.DataDir, "logs")
	appLogger, ringBuf, logCloser, err := logger.InitLogger(cfg.LogLevel, logDir)
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	if logCloser != nil {
		defer logCloser.Close()
	}

	appLogger.Info("MediaVault starting",
		"data_dir", cfg.DataDir,
		"listen", cfg.Listen,
		"log_level", cfg.LogLevel,
	)

	// 3. Acquire exclusive process lock
	lockPath := filepath.Join(cfg.DataDir, ".mediavault.lock")
	fileLock, err := db.AcquireExclusiveLock(lockPath)
	if err != nil {
		return fmt.Errorf("acquire process lock: %w", err)
	}
	defer fileLock.Release()

	// 4. Initialize or load master key
	if err := cfg.InitMasterKey(); err != nil {
		return fmt.Errorf("init master key: %w", err)
	}
	appLogger.Info("Master key loaded successfully", "key_file", cfg.MasterKeyFile)

	// 5. Open SQLite database pools
	dbPath := filepath.Join(cfg.DataDir, "mediavault.db")
	database, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open sqlite database: %w", err)
	}
	defer database.Close()

	// 6. Probe SQLite capabilities
	ctxProbe, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelProbe()

	caps, err := db.ProbeCapabilities(ctxProbe, database.Writer())
	if err != nil {
		return fmt.Errorf("probe sqlite capabilities: %w", err)
	}
	appLogger.Info("SQLite capabilities verified",
		"version", caps.Version,
		"json", caps.HasJSON,
		"wal", caps.JournalMode,
		"foreign_keys", caps.ForeignKeysEnabled,
	)

	// 7. Check / initialize database schema
	schemaMeta, err := db.InitEmptyDatabase(context.Background(), database)
	if err != nil {
		if errors.Is(err, db.ErrLegacyV0Database) {
			appLogger.Warn("Detected legacy-v0 database schema. Migration required.")
		} else {
			return fmt.Errorf("initialize database schema: %w", err)
		}
	}

	var serverID string
	if schemaMeta != nil {
		serverID = schemaMeta.ServerID
		appLogger.Info("Database schema verified",
			"version", schemaMeta.Version,
			"server_id", schemaMeta.ServerID,
		)
	}

	// 8. Bootstrap admin password check
	adminPwd, isNewBootstrap, err := cfg.GetOrGenerateAdminBootstrapPassword()
	if err != nil {
		return fmt.Errorf("admin password check: %w", err)
	}
	if isNewBootstrap {
		appLogger.Warn("Bootstrap admin password generated. Please check data/bootstrap-password and change password upon first login.")
		_ = adminPwd // Used during admin creation in P1
	}

	// 9. Initialize Lifecycle Manager
	lifecycle := services.NewLifecycleManager(database)
	if schemaMeta != nil {
		lifecycle.SetReady(true, serverID, "")
	} else {
		lifecycle.SetReady(false, "", "database schema requires migration")
	}

	// 10. Build HTTP server with Gin
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(middleware.RequestID())
	engine.Use(middleware.NewBoundedQueueLimiter(1000).Handler())

	// Health and readiness endpoints
	engine.GET("/healthz", lifecycle.HandleHealthz)
	engine.GET("/readyz", lifecycle.HandleReadyz)

	// Web frontend static files
	if err := web.RegisterWebRoutes(engine); err != nil {
		return fmt.Errorf("register web routes: %w", err)
	}

	httpSrv := &http.Server{
		Addr:         cfg.Listen,
		Handler:      engine,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start server in background
	serverErrChan := make(chan error, 1)
	go func() {
		appLogger.Info("MediaVault HTTP server listening", "addr", cfg.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrChan <- err
		}
	}()

	// 11. Handle OS signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrChan:
		return fmt.Errorf("http server failed: %w", err)
	case sig := <-sigChan:
		appLogger.Info("Shutdown signal received, shutting down gracefully", "signal", sig.String())
	}

	// 12. Graceful shutdown
	if err := lifecycle.Shutdown(httpSrv, 10*time.Second); err != nil {
		appLogger.Error("Error during graceful shutdown", "error", err)
	} else {
		appLogger.Info("MediaVault stopped cleanly")
	}

	_ = ringBuf
	return nil
}
