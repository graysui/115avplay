package services

import (
	"context"
	"fmt"
	"log/slog"
	"mediavault/internal/client115"
	"mediavault/internal/db"
	"mediavault/internal/models"
	"time"
)

type JanitorService struct {
	database    *db.DB
	assetRepo   *db.AssetRepo
	c115Client  *client115.Client
	tempRootCID string
	logger      *slog.Logger
}

func NewJanitorService(
	database *db.DB,
	assetRepo *db.AssetRepo,
	c115Client *client115.Client,
	tempRootCID string,
	logger *slog.Logger,
) *JanitorService {
	if logger == nil {
		logger = slog.Default()
	}
	return &JanitorService{
		database:    database,
		assetRepo:   assetRepo,
		c115Client:  c115Client,
		tempRootCID: tempRootCID,
		logger:      logger,
	}
}

// RunCleanup processes expired temporary assets.
func (j *JanitorService) RunCleanup(ctx context.Context, cleanupEnabled bool) (cleaned int, skipped int, err error) {
	if !cleanupEnabled {
		j.logger.Info("cleanup is disabled by configuration, skipping deletion submission")
		return 0, 0, nil
	}

	now := models.UTCNow()
	expiredAssets, err := j.assetRepo.ListExpiredTemporaryAssets(ctx, now)
	if err != nil {
		return 0, 0, fmt.Errorf("list expired temporary assets: %w", err)
	}

	for _, a := range expiredAssets {
		// 1. Play lease check: do not delete if active session lease exists
		hasLease, err := j.assetRepo.HasActivePlayLease(ctx, a.ID, now)
		if err != nil || hasLease {
			j.logger.Info("skipping deletion of asset with active play lease", "asset_id", a.ID)
			skipped++
			continue
		}

		// 2. Ownership & parent chain validation
		if a.OwnedRootID == nil || *a.OwnedRootID == "" || *a.OwnedRootID == "0" || *a.OwnedRootID == j.tempRootCID {
			// Cannot delete root or empty
			j.logger.Warn("asset has invalid owned_root_id, quarantining", "asset_id", a.ID)
			_ = j.assetRepo.UpdateAssetState(ctx, a.ID, "quarantined", nil)
			continue
		}

		targetCID := *a.OwnedRootID

		// 3. Mark pending_delete
		_ = j.assetRepo.UpdateAssetState(ctx, a.ID, "pending_delete", nil)

		// 4. Submit deletion to 115
		err = j.c115Client.DeleteFiles(ctx, []string{targetCID})
		if err != nil {
			j.logger.Error("delete submission failed on 115", "asset_id", a.ID, "cid", targetCID, "error", err)
			errMsg := err.Error()
			_ = j.assetRepo.UpdateAssetState(ctx, a.ID, "ready", &errMsg) // revert
			continue
		}

		// 5. Mark deleting
		_ = j.assetRepo.UpdateAssetState(ctx, a.ID, "deleting", nil)

		// 6. Confirm missing
		confirmCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		missing, _ := j.c115Client.ConfirmMissing(confirmCtx, j.tempRootCID, targetCID)
		cancel()

		if missing {
			_ = j.assetRepo.UpdateAssetState(ctx, a.ID, "deleted", nil)
			j.logger.Info("asset successfully deleted and confirmed missing", "asset_id", a.ID, "cid", targetCID)
			cleaned++
		} else {
			j.logger.Warn("asset deletion accepted but not yet confirmed missing", "asset_id", a.ID, "cid", targetCID)
		}
	}

	return cleaned, skipped, nil
}
