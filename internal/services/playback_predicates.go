package services

import (
	"mediavault/internal/models"
	"strings"
	"time"
)

// IsAssetPlayable returns true if an asset meets real-time playability rules according to design §8.0.
// For a new playback session:
// - asset.State == "ready"
// - file_id and pick_code are non-empty
// - permanent: expires_at is NULL
// - temporary: expires_at is non-NULL AND expires_at > now
func IsAssetPlayable(asset *models.CloudAsset, now time.Time) bool {
	if asset == nil {
		return false
	}
	if asset.State != "ready" {
		return false
	}
	if asset.FileID == nil || strings.TrimSpace(*asset.FileID) == "" {
		return false
	}
	if asset.PickCode == nil || strings.TrimSpace(*asset.PickCode) == "" {
		return false
	}

	if asset.SourceType == "permanent" {
		return asset.ExpiresAt == nil
	}

	if asset.SourceType == "temporary" {
		if asset.ExpiresAt == nil {
			return false
		}
		exp, err := time.Parse(time.RFC3339, *asset.ExpiresAt)
		if err != nil {
			return false
		}
		return exp.After(now)
	}

	return false
}

// CanSessionContinue checks whether an ongoing playback session can continue requesting CDN streams.
// As long as the session's play lease is active (lease_until > now) and the asset is not confirmed missing,
// playback can continue even if the temporary asset's TTL has expired for new sessions.
func CanSessionContinue(session *models.PlaySession, asset *models.CloudAsset, now time.Time) bool {
	if session == nil || asset == nil {
		return false
	}
	if asset.State == "missing" || asset.State == "deleted" {
		return false
	}
	if session.State != "active" && session.State != "negotiating" {
		return false
	}
	if session.LeaseUntil == nil {
		return false
	}
	leaseExp, err := time.Parse(time.RFC3339, *session.LeaseUntil)
	if err != nil {
		return false
	}
	return leaseExp.After(now)
}
