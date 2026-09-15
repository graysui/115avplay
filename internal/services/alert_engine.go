package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/models"
)

// AlertEngine manages persistent system alerts and asynchronous webhook delivery.
type AlertEngine struct {
	settingsRepo *db.SettingsRepo
	appConfig    *config.AppConfig
	httpClient   *http.Client
	logger       *slog.Logger
}

func NewAlertEngine(
	settingsRepo *db.SettingsRepo,
	appConfig *config.AppConfig,
	logger *slog.Logger,
) *AlertEngine {
	if logger == nil {
		logger = slog.Default()
	}
	return &AlertEngine{
		settingsRepo: settingsRepo,
		appConfig:    appConfig,
		httpClient: &http.Client{
			Timeout: 5 * time.Second, // Delivery timeout 5 seconds per CONFIGURATION.md
		},
		logger: logger,
	}
}

// TriggerAlert activates or updates an alert and asynchronously delivers a webhook notification.
func (e *AlertEngine) TriggerAlert(ctx context.Context, key, message string) error {
	now := models.UTCNow()
	alert := &models.Alert{
		Key:         key,
		State:       "active",
		Message:     message,
		LastSeenAt:  now,
		FirstSeenAt: now,
	}

	// 1. Persist alert to database
	if err := e.settingsRepo.UpsertAlert(ctx, alert); err != nil {
		return fmt.Errorf("upsert alert %s: %w", key, err)
	}

	// 2. Deliver webhook in background without blocking caller
	go e.deliverWebhook(alert)

	return nil
}

// ResolveAlert marks an active alert as resolved and notifies webhook if configured.
func (e *AlertEngine) ResolveAlert(ctx context.Context, key string) error {
	if err := e.settingsRepo.ResolveAlert(ctx, key); err != nil {
		return fmt.Errorf("resolve alert %s: %w", key, err)
	}

	resolvedAlert := &models.Alert{
		Key:        key,
		State:      "resolved",
		Message:    "Alert resolved",
		LastSeenAt: models.UTCNow(),
	}

	go e.deliverWebhook(resolvedAlert)
	return nil
}

func (e *AlertEngine) deliverWebhook(alert *models.Alert) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Get webhook URL
	var webhookURL string
	if e.appConfig != nil && e.appConfig.MasterKey != nil {
		decrypted, err := e.settingsRepo.GetDecryptedSecret(ctx, e.appConfig.MasterKey, "alert_webhook")
		if err == nil {
			webhookURL = strings.TrimSpace(decrypted)
		}
	}

	if webhookURL == "" {
		return // No webhook configured
	}

	// 2. Check silence period
	silenceSec := 3600
	if s, err := e.settingsRepo.GetSetting(ctx, "alert_silence_sec"); err == nil && s != nil && s.Value != nil {
		if val, err := strconv.Atoi(*s.Value); err == nil && val >= 60 {
			silenceSec = val
		}
	}

	// If alert was delivered recently within silence window and is still active, skip
	if alert.State == "active" && alert.LastDeliveredAt != nil && *alert.LastDeliveredAt != "" {
		if t, err := time.Parse(time.RFC3339, *alert.LastDeliveredAt); err == nil {
			if time.Since(t) < time.Duration(silenceSec)*time.Second {
				return
			}
		}
	}

	// 3. Build payload
	payload := map[string]interface{}{
		"key":        alert.Key,
		"state":      alert.State,
		"message":    alert.Message,
		"timestamp":  models.UTCNow(),
		"event_type": "mediavault_alert",
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "MediaVault-AlertEngine/1.0")

	// 4. Send request
	now := models.UTCNow()
	resp, err := e.httpClient.Do(req)

	alert.DeliveryAttempts++
	if err != nil {
		errMsg := err.Error()
		alert.DeliveryError = &errMsg
		e.logger.Warn("Failed to deliver alert webhook", "key", alert.Key, "error", err.Error())
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			alert.LastDeliveredAt = &now
			alert.DeliveryError = nil
		} else {
			statusErr := fmt.Sprintf("HTTP %d", resp.StatusCode)
			alert.DeliveryError = &statusErr
		}
	}

	// Update delivery status in DB
	_ = e.settingsRepo.UpsertAlert(ctx, alert)
}

// Alert helper methods
func (e *AlertEngine) AlertTransferUnknown(ctx context.Context, jobID string, detail string) {
	_ = e.TriggerAlert(ctx, "transfer_unknown:"+jobID, fmt.Sprintf("Transfer job %s entered unknown/reconcile state: %s", jobID, detail))
}

func (e *AlertEngine) AlertJanitorQuarantine(ctx context.Context, assetID string, reason string) {
	_ = e.TriggerAlert(ctx, "janitor_quarantine:"+assetID, fmt.Sprintf("Cloud asset %s quarantined by janitor: %s", assetID, reason))
}

func (e *AlertEngine) AlertSyncGap(ctx context.Context, releaseID string) {
	_ = e.TriggerAlert(ctx, "sync_gap:"+releaseID, fmt.Sprintf("Sync gap detected for release %s", releaseID))
}

func (e *AlertEngine) AlertDiskLow(ctx context.Context, freeBytes, minBytes uint64) {
	_ = e.TriggerAlert(ctx, "disk_space_low", fmt.Sprintf("Disk free space (%d MB) is below required minimum threshold (%d MB)", freeBytes/(1024*1024), minBytes/(1024*1024)))
}

func (e *AlertEngine) Alert115AuthFailed(ctx context.Context, reason string) {
	_ = e.TriggerAlert(ctx, "cloud115_auth_failed", fmt.Sprintf("115 cloud authentication or refresh failed: %s", reason))
}
