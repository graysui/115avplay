package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"mediavault/internal/client115"
	"mediavault/internal/db"
	"mediavault/internal/models"
	"time"

	"github.com/google/uuid"
)

var (
	ErrResourceUnavailable = errors.New("resource_unavailable: no playable or transferable candidate version found")
	ErrResourcePreparing   = errors.New("resource_preparing: resource is downloading, please retry later")
)

type PlaybackResolution struct {
	SessionID   string
	StreamURL   string
	MovieCode   string
	InfoHash    string
	AssetID     string
	Container   string
	SizeBytes   int64
	ExpiresAt   *string
	IsDirectPlay bool
}

type Resolver struct {
	database        *db.DB
	assetRepo       *db.AssetRepo
	magnetRepo      *db.MagnetRepo
	transferManager *TransferManager
	c115Client      *client115.Client
	streamWaitMs    int
	leaseSec        int
	logger          *slog.Logger
}

func NewResolver(
	database *db.DB,
	assetRepo *db.AssetRepo,
	magnetRepo *db.MagnetRepo,
	transferManager *TransferManager,
	c115Client *client115.Client,
	streamWaitMs int,
	leaseSec int,
	logger *slog.Logger,
) *Resolver {
	if streamWaitMs <= 0 {
		streamWaitMs = 8000 // 8 seconds
	}
	if leaseSec <= 0 {
		leaseSec = 120 // 2 minutes
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Resolver{
		database:        database,
		assetRepo:       assetRepo,
		magnetRepo:      magnetRepo,
		transferManager: transferManager,
		c115Client:      c115Client,
		streamWaitMs:    streamWaitMs,
		leaseSec:        leaseSec,
		logger:          logger,
	}
}

// ResolvePlayback resolves a playable stream URL according to design §7.1 and §8.0.
func (r *Resolver) ResolvePlayback(ctx context.Context, movieCode, explicitSourceID, userID, deviceID string) (*PlaybackResolution, error) {
	now := models.UTCNow()
	nowTime := time.Now().UTC()

	// 1. Check binding
	binding, err := r.assetRepo.GetActiveBinding(ctx, "115")
	if err != nil || binding == nil {
		return nil, fmt.Errorf("active 115 binding not found")
	}

	var targetMagnet *models.Magnet
	var targetAsset *models.CloudAsset
	var tier int

	if explicitSourceID != "" {
		// Explicit version selection: must resolve this version only, no silent switching!
		magnets, err := r.magnetRepo.ListMagnetsByMovie(ctx, movieCode)
		if err != nil {
			return nil, err
		}
		for _, m := range magnets {
			if m.InfoHash == explicitSourceID {
				targetMagnet = &m
				break
			}
		}
		if targetMagnet == nil || targetMagnet.Enabled == 0 {
			return nil, ErrResourceUnavailable
		}

		// Check if asset is ready for this explicit magnet
		targetAsset, _ = r.assetRepo.GetReadyAssetByResource(ctx, targetMagnet.InfoHash, binding.ID, now)
		if targetAsset != nil && IsAssetPlayable(targetAsset, nowTime) {
			tier = 1
			if targetAsset.SourceType == "temporary" {
				tier = 2
			}
		} else {
			tier = 3
		}
	} else {
		// Default resolution using 3-tier hierarchy
		resolved, err := r.magnetRepo.ResolveDefaultSource(ctx, movieCode, now)
		if err != nil || resolved == nil || resolved.Magnet == nil {
			return nil, ErrResourceUnavailable
		}
		tier = resolved.Tier
		targetMagnet = resolved.Magnet
		targetAsset = resolved.CloudAsset
	}

	// 2. If Tier 1 or Tier 2 (Ready Asset available)
	if (tier == 1 || tier == 2) && targetAsset != nil && IsAssetPlayable(targetAsset, nowTime) {
		return r.createPlaySessionAndURL(ctx, targetMagnet, targetAsset, userID, deviceID)
	}

	// 3. If Tier 3 (Cold transferable magnet): trigger transfer and wait up to streamWaitMs
	if tier == 3 && targetMagnet != nil && (targetMagnet.ResourceKind == "btih" || targetMagnet.ResourceKind == "ed2k") {
		r.logger.Info("initiating transfer for cold magnet", "movie_code", movieCode, "info_hash", targetMagnet.InfoHash)
		_, err := r.transferManager.StartTransfer(ctx, binding, targetMagnet)
		if err != nil {
			r.logger.Warn("start transfer error", "error", err)
		}

		// Wait loop up to streamWaitMs
		waitDeadline := time.Now().Add(time.Duration(r.streamWaitMs) * time.Millisecond)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for time.Now().Before(waitDeadline) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-ticker.C:
				asset, _ := r.assetRepo.GetReadyAssetByResource(ctx, targetMagnet.InfoHash, binding.ID, models.UTCNow())
				if asset != nil && IsAssetPlayable(asset, time.Now().UTC()) {
					return r.createPlaySessionAndURL(ctx, targetMagnet, asset, userID, deviceID)
				}
			}
		}

		// Timed out: return 503 resource_preparing
		return nil, ErrResourcePreparing
	}

	return nil, ErrResourceUnavailable
}

func (r *Resolver) createPlaySessionAndURL(ctx context.Context, magnet *models.Magnet, asset *models.CloudAsset, userID, deviceID string) (*PlaybackResolution, error) {
	now := models.UTCNow()
	leaseUntil := time.Now().UTC().Add(time.Duration(r.leaseSec) * time.Second).Format(time.RFC3339)

	// Fetch 115 download CDN URL
	if asset.PickCode == nil || *asset.PickCode == "" {
		return nil, fmt.Errorf("asset has no pick_code")
	}

	downURLInfo, err := r.c115Client.GetDownloadURL(ctx, *asset.PickCode)
	if err != nil {
		return nil, fmt.Errorf("get download url from 115: %w", err)
	}

	sessionID := "play_" + uuid.New().String()[:8]

	// Register play lease in play_sessions
	err = r.database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO play_sessions (
				id, user_id, movie_code, resource_key, asset_id, device_id, state,
				position_ticks, lease_until, started_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, 'active', 0, ?, ?, ?);
		`, sessionID, userID, magnet.MovieCode, magnet.InfoHash, asset.ID, deviceID, leaseUntil, now, now)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("register play session: %w", err)
	}

	container := "mp4"
	if asset.Container != nil && *asset.Container != "" {
		container = *asset.Container
	}

	return &PlaybackResolution{
		SessionID:    sessionID,
		StreamURL:    downURLInfo.URL,
		MovieCode:    magnet.MovieCode,
		InfoHash:     magnet.InfoHash,
		AssetID:      asset.ID,
		Container:    container,
		SizeBytes:    downURLInfo.FileSize,
		ExpiresAt:    asset.ExpiresAt,
		IsDirectPlay: true,
	}, nil
}
