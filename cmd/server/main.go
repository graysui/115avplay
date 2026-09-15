package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"mediavault/internal/api/admin"
	"mediavault/internal/api/middleware"
	"mediavault/internal/app"
	"mediavault/internal/client115"
	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/emby"
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
			appLogger.Warn("检测到 legacy-v0 旧数据库，开始自动迁移（迁移前会自动备份原库）")
			migrator := db.NewMigrator(database)
			schemaMeta, err = migrator.MigrateLegacyV0(context.Background())
			if err != nil {
				return fmt.Errorf("migrate legacy database: %w", err)
			}
			appLogger.Info("legacy-v0 数据库迁移完成", "version", schemaMeta.Version, "server_id", schemaMeta.ServerID)
			err = nil
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

	// 8. Bootstrap repositories
	userRepo := db.NewUserRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	movieRepo := db.NewMovieRepo(database)
	magnetRepo := db.NewMagnetRepo(database)
	assetRepo := db.NewAssetRepo(database)
	libraryRepo := db.NewLibraryRepo(database)
	jobRepo := db.NewJobRepo(database)
	progressRepo := db.NewProgressRepo(database)

	// Clean up jobs left behind by a previous process (crash / restart).
	if n, err := jobRepo.FailOrphanedJobs(context.Background()); err != nil {
		appLogger.Warn("清理中断任务失败", "错误", err.Error())
	} else if n > 0 {
		appLogger.Warn("已清理上次中断的任务", "数量", n)
	}

	// Fill in missing covers from the offline database's preview images
	// (FC2/素人 and 国产 libraries are never scraped by JavDB).
	if n, err := movieRepo.BackfillCoversFromPreview(context.Background()); err != nil {
		appLogger.Warn("从预览图补全封面失败", "错误", err.Error())
	} else if n > 0 {
		appLogger.Info("已从离线库预览图补全封面", "数量", n)
	}

	// Ensure the three ranking-based virtual libraries exist (周榜/月榜/TOP250).
	if changed, err := db.EnsureRankingLibraries(context.Background(), database); err != nil {
		appLogger.Warn("初始化榜单媒体库失败", "错误", err.Error())
	} else if changed {
		appLogger.Info("已初始化榜单媒体库（周榜/月榜/TOP250）")
	}

	// One-time: mark the migrated legacy library as already scraped so it is not
	// re-queued for JavDB. Only newly ingested movies stay pending and get scraped.
	if s, _ := settingsRepo.GetSetting(context.Background(), "legacy_scrape_normalized"); s == nil || s.Value == nil || *s.Value == "" {
		if n, err := movieRepo.MarkLegacyCompleteAsScraped(context.Background()); err != nil {
			appLogger.Warn("标记旧库为已刮削失败", "错误", err.Error())
		} else {
			appLogger.Info("旧库已标记为已刮削（不再重复刮削）", "数量", n)
		}
		_, _ = settingsRepo.UpdateSettings(context.Background(), 0, map[string]string{"legacy_scrape_normalized": "true"}, nil, nil)
	}

	// 8.5 Initialize the 115 OpenAPI client and OAuth auth client.
	// Only the App ID (client_id) is required for the device-code flow; it comes from
	// MV_115_CLIENT_ID or the (persisted) database setting manageable from the admin UI.
	client115ID := cfg.Client115ID
	if client115ID == "" {
		if s, err := settingsRepo.GetSetting(context.Background(), "115_client_id"); err == nil && s != nil && s.Value != nil {
			client115ID = *s.Value
		}
	}

	c115Client, err := client115.NewClient(client115.ClientConfig{})
	if err != nil {
		return fmt.Errorf("init 115 client: %w", err)
	}
	// Load the optional web cookie so download links and tree scans can use the
	// cookie web API even when the OAuth access token has expired.
	if cookie, cerr := settingsRepo.GetDecryptedSecret(context.Background(), cfg.MasterKey, "115_cookie"); cerr == nil && cookie != "" {
		c115Client.SetCookie(cookie)
	}
	auth115 := client115.NewAuthClient(c115Client, client115ID)

	// Refresh the OAuth token automatically when an API call reports an auth error.
	c115Client.SetTokenRefresher(func(ctx context.Context) error {
		td, rerr := auth115.RefreshToken(ctx)
		if rerr != nil {
			return rerr
		}
		// Persist the rotated tokens so they survive restarts.
		if b, e := assetRepo.GetActiveBinding(ctx, "115"); e == nil && b != nil && b.SecretSettingKey != "" {
			if data, jerr := json.Marshal(td); jerr == nil {
				if env, eerr := cfg.MasterKey.Encrypt(b.SecretSettingKey, data); eerr == nil {
					_, _ = settingsRepo.UpdateSettings(ctx, 0, nil, map[string]string{b.SecretSettingKey: env}, nil)
				}
			}
		}
		return nil
	})

	// Restore previously persisted OAuth tokens, if any.
	if binding, berr := assetRepo.GetActiveBinding(context.Background(), "115"); berr == nil && binding != nil && binding.SecretSettingKey != "" {
		if raw, derr := settingsRepo.GetDecryptedSecret(context.Background(), cfg.MasterKey, binding.SecretSettingKey); derr == nil && raw != "" {
			var td client115.TokenData
			if json.Unmarshal([]byte(raw), &td) == nil && td.AccessToken != "" {
				auth115.SetTokens(&td)
				appLogger.Info("115 tokens restored", "provider_user_id", binding.ProviderUserID, "has_refresh_token", td.RefreshToken != "")
			}
		}
	}

	// Resolve the temp transfer root (env override wins over DB setting).
	tempTransferCID := ""
	if v, ok := cfg.EnvOverrides["temp_transfer_cid"]; ok {
		tempTransferCID = v
	} else if s, err := settingsRepo.GetSetting(context.Background(), "temp_transfer_cid"); err == nil && s != nil && s.Value != nil {
		tempTransferCID = *s.Value
	}

	// 9. Bootstrap admin password check & create default admin
	adminPwd, isNewBootstrap, err := cfg.GetOrGenerateAdminBootstrapPassword()
	if err != nil {
		return fmt.Errorf("admin password check: %w", err)
	}
	_, err = userRepo.EnsureAdminUser(context.Background(), "admin", adminPwd, isNewBootstrap)
	if err != nil {
		return fmt.Errorf("ensure admin user: %w", err)
	}
	if isNewBootstrap {
		appLogger.Warn("Bootstrap admin password generated. Please check data/bootstrap-password and change password upon first login.")
	}

	// 10. Initialize Core Domain Services
	transferManager := services.NewTransferManager(database, assetRepo, magnetRepo, jobRepo, c115Client, tempTransferCID, 7, appLogger)
	janitor := services.NewJanitorService(database, assetRepo, c115Client, tempTransferCID, appLogger)
	_ = janitor
	resolver := services.NewResolver(database, assetRepo, magnetRepo, transferManager, c115Client, 8000, 120, appLogger)

	// 10.5 Application runtime wiring background task executors (JavDB, ingestion, scan).
	runtime := app.New(cfg, appLogger, database, settingsRepo, movieRepo, magnetRepo, assetRepo, jobRepo, c115Client, auth115)
	runtime.Start(context.Background())

	// 11. Initialize Lifecycle Manager
	lifecycle := services.NewLifecycleManager(database)
	if schemaMeta != nil {
		lifecycle.SetReady(true, serverID, "")
	} else {
		lifecycle.SetReady(false, "", "database schema requires migration")
	}

	// 12. Build HTTP server with Gin
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(middleware.RequestID())
	engine.Use(middleware.RequestLogger(appLogger))
	engine.Use(middleware.NewBoundedQueueLimiter(1000).Handler())

	// Health and readiness endpoints
	engine.GET("/healthz", lifecycle.HandleHealthz)
	engine.GET("/readyz", lifecycle.HandleReadyz)

	// 13. Mount Admin APIs under /api/v1
	apiV1 := engine.Group("/api/v1")
	admin.RegisterAdminRoutes(apiV1, userRepo, settingsRepo, movieRepo, magnetRepo, assetRepo, libraryRepo, jobRepo, database, cfg, transferManager, auth115, runtime, runtime)

	// 14. Mount Emby Server handler
	cacheDir := filepath.Join(cfg.DataDir, "cache", "images")
	embyServer, err := emby.NewServer(database, movieRepo, magnetRepo, assetRepo, userRepo, progressRepo, libraryRepo, resolver, cacheDir, cfg.PublicURL, appLogger)
	if err != nil {
		return fmt.Errorf("init emby server: %w", err)
	}
	embyHandler := gin.WrapH(embyServer)
	engine.Any("/emby/*path", embyHandler)
	engine.Any("/emby", embyHandler)
	// Direct public endpoints accessed by Emby clients without prefix
	engine.Any("/System/Info/Public", embyHandler)
	engine.Any("/system/info/public", embyHandler)
	engine.Any("/System/Endpoint", embyHandler)
	engine.Any("/system/endpoint", embyHandler)
	engine.Any("/Users/AuthenticateByName", embyHandler)
	engine.Any("/users/authenticatebyname", embyHandler)

	// 15. Web frontend static files & SPA fallback
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

	// 16. Handle OS signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErrChan:
		return fmt.Errorf("http server failed: %w", err)
	case sig := <-sigChan:
		appLogger.Info("Shutdown signal received, shutting down gracefully", "signal", sig.String())
	}

	// 17. Graceful shutdown
	if err := lifecycle.Shutdown(httpSrv, 10*time.Second); err != nil {
		appLogger.Error("Error during graceful shutdown", "error", err)
	} else {
		appLogger.Info("MediaVault stopped cleanly")
	}

	_ = ringBuf
	return nil
}
