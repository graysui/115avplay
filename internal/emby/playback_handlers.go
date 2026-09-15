package emby

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"mediavault/internal/db"
	"mediavault/internal/models"
	"mediavault/internal/services"

	"github.com/google/uuid"
)

type PlaybackHandlers struct {
	serverID     string
	database     *db.DB
	movieRepo    *db.MovieRepo
	magnetRepo   *db.MagnetRepo
	assetRepo    *db.AssetRepo
	progressRepo *db.ProgressRepo
	resolver     *services.Resolver
	logger       *slog.Logger
}

func NewPlaybackHandlers(
	serverID string,
	database *db.DB,
	movieRepo *db.MovieRepo,
	magnetRepo *db.MagnetRepo,
	assetRepo *db.AssetRepo,
	progressRepo *db.ProgressRepo,
	resolver *services.Resolver,
	logger *slog.Logger,
) *PlaybackHandlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &PlaybackHandlers{
		serverID:     serverID,
		database:     database,
		movieRepo:    movieRepo,
		magnetRepo:   magnetRepo,
		assetRepo:    assetRepo,
		progressRepo: progressRepo,
		resolver:     resolver,
		logger:       logger,
	}
}

type playbackInfoRequest struct {
	MediaSourceId string `json:"MediaSourceId"`
}

// GetPlaybackInfo handles GET/POST /items/{id}/playbackinfo.
func (h *PlaybackHandlers) GetPlaybackInfo(w http.ResponseWriter, r *http.Request, rawID string) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	movieCode, err := DecodeItemID(rawID)
	if err != nil {
		http.Error(w, `{"error":"invalid item id"}`, http.StatusNotFound)
		return
	}

	movie, err := h.movieRepo.GetMovie(r.Context(), movieCode)
	if err != nil || movie == nil || movie.DeletedAt != nil {
		h.logger.Warn("播放信息请求失败：影片不存在", "影片", movieCode)
		http.Error(w, `{"error":"item not found"}`, http.StatusNotFound)
		return
	}

	var reqBody playbackInfoRequest
	if r.Method == "POST" && r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
	}

	sourceID := strings.TrimSpace(GetQueryParam(r, "mediasourceid"))
	if sourceID == "" {
		sourceID = strings.TrimSpace(reqBody.MediaSourceId)
	}

	var explicitInfoHash string
	if sourceID != "" {
		explicitInfoHash, _ = DecodeSourceID(sourceID)
	}

	// Create negotiating play session
	sessionID := "play_" + uuid.New().String()[:8]
	now := models.UTCNow()
	leaseUntil := time.Now().UTC().Add(120 * time.Second).Format(time.RFC3339)

	deviceID := "dev_emby"
	if authSess := SessionFromContext(r.Context()); authSess != nil && authSess.DeviceID != "" {
		deviceID = authSess.DeviceID
	}

	var resKey *string
	if explicitInfoHash != "" {
		resKey = &explicitInfoHash
	}

	err = h.database.ExecWrite(r.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(r.Context(), `
			INSERT INTO play_sessions (
				id, user_id, movie_code, resource_key, device_id, state,
				position_ticks, lease_until, started_at, updated_at
			) VALUES (?, ?, ?, ?, ?, 'negotiating', 0, ?, ?, ?);
		`, sessionID, user.ID, movieCode, resKey, deviceID, leaseUntil, now, now)
		return err
	})
	if err != nil {
		http.Error(w, `{"error":"failed to create play session"}`, http.StatusInternalServerError)
		return
	}

	// Build sources
	magnets, _ := h.magnetRepo.ListMagnetsByMovie(r.Context(), movieCode)
	var sources []MediaSourceDTO
	nowTime := time.Now().UTC()

	binding, _ := h.assetRepo.GetActiveBinding(r.Context(), "115")
	var bindingID string
	if binding != nil {
		bindingID = binding.ID
	}

	for _, m := range magnets {
		if m.Enabled != 1 {
			continue
		}
		if explicitInfoHash != "" && m.InfoHash != explicitInfoHash {
			continue
		}

		sID := EncodeSourceID(m.InfoHash)
		// Relative to the Emby server base URL. Including the "/emby" prefix here
		// causes clients (e.g. Hills) to request /emby/emby/videos/... .
		streamURL := fmt.Sprintf("/videos/%s/stream?MediaSourceId=%s", rawID, sID)

		playable := false
		if bindingID != "" {
			asset, _ := h.assetRepo.GetReadyAssetByResource(r.Context(), m.InfoHash, bindingID, now)
			if asset != nil && services.IsAssetPlayable(asset, nowTime) {
				playable = true
			}
		}

		name := m.QualityLabel
		if m.HasChineseSub == 1 {
			name += " 中文字幕"
		}
		if m.Title != nil && *m.Title != "" {
			name += " - " + *m.Title
		}

		sources = append(sources, MediaSourceDTO{
			Id:                   sID,
			Name:                 name,
			Path:                 streamURL,
			DirectStreamUrl:      streamURL,
			Protocol:             "Http",
			Container:            "mp4",
			Size:                 m.SizeBytes,
			RunTimeTicks:         movie.RuntimeTicks,
			SupportsDirectPlay:   playable,
			SupportsDirectStream: false,
			SupportsTranscoding:  false,
			MediaStreams: []MediaStreamDTO{
				{
					Type:         "Video",
					DisplayTitle: m.QualityLabel,
					Codec:        "h264",
					Index:        0,
				},
			},
		})
	}

	writeJSON(w, http.StatusOK, PlaybackInfoResponse{
		MediaSources:  sources,
		PlaySessionId: sessionID,
	})
	h.logger.Info("播放信息协商完成", "影片", movieCode, "用户", user.ID, "会话", sessionID, "版本数", len(sources))
}

// StreamHandler handles GET and HEAD /videos/{id}/stream and /videos/{id}/stream.{container}.
func (h *PlaybackHandlers) StreamHandler(w http.ResponseWriter, r *http.Request, rawID string) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	movieCode, err := DecodeItemID(rawID)
	if err != nil {
		http.Error(w, `{"error":"invalid item id"}`, http.StatusNotFound)
		return
	}

	sourceID := strings.TrimSpace(GetQueryParam(r, "mediasourceid"))
	var explicitInfoHash string
	if sourceID != "" {
		explicitInfoHash, _ = DecodeSourceID(sourceID)
	}

	deviceID := "dev_stream"
	if authSess := SessionFromContext(r.Context()); authSess != nil && authSess.DeviceID != "" {
		deviceID = authSess.DeviceID
	}

	// HEAD request: check readiness without triggering new tasks
	if r.Method == "HEAD" {
		binding, _ := h.assetRepo.GetActiveBinding(r.Context(), "115")
		if binding == nil {
			h.logger.Warn("流媒体 HEAD 失败：未绑定 115 账号", "影片", movieCode)
			http.Error(w, `{"error":"binding not available"}`, http.StatusServiceUnavailable)
			return
		}
		now := models.UTCNow()
		var targetAsset *models.CloudAsset
		if explicitInfoHash != "" {
			targetAsset, _ = h.assetRepo.GetReadyAssetByResource(r.Context(), explicitInfoHash, binding.ID, now)
		} else {
			resolved, _ := h.magnetRepo.ResolveDefaultSource(r.Context(), movieCode, now)
			if resolved != nil {
				targetAsset = resolved.CloudAsset
			}
		}

		if targetAsset != nil && services.IsAssetPlayable(targetAsset, time.Now().UTC()) {
			// Ready: return 302 redirect header
			res, err := h.resolver.ResolvePlayback(r.Context(), movieCode, explicitInfoHash, user.ID, deviceID)
			if err == nil && res != nil {
				h.logger.Info("流媒体 HEAD 就绪（302）", "影片", movieCode, "版本", explicitInfoHash)
				w.Header().Set("Location", res.StreamURL)
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusFound)
				return
			}
		}

		// Cold/not ready: HEAD returns 503
		h.logger.Info("流媒体 HEAD 未就绪（503，等待转存）", "影片", movieCode, "版本", explicitInfoHash)
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	// GET request: call Resolver
	res, err := h.resolver.ResolvePlayback(r.Context(), movieCode, explicitInfoHash, user.ID, deviceID)
	if err != nil {
		if errors.Is(err, services.ErrResourcePreparing) {
			h.logger.Info("流媒体 GET：资源准备中（503）", "影片", movieCode, "版本", explicitInfoHash)
			w.Header().Set("Retry-After", "5")
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"error": map[string]string{
					"code": "resource_preparing",
				},
			})
			return
		}
		h.logger.Warn("流媒体 GET 失败：资源不可用（404）", "影片", movieCode, "版本", explicitInfoHash, "错误", err.Error())
		http.Error(w, `{"error":"resource unavailable"}`, http.StatusNotFound)
		return
	}

	// 302 Redirect to CDN download URL
	h.logger.Info("流媒体 GET：302 跳转到 115 CDN", "影片", movieCode, "版本", res.InfoHash, "资产类型", res.AssetID)
	w.Header().Set("Location", res.StreamURL)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
}

type sessionReportRequest struct {
	ItemId        string `json:"ItemId"`
	MediaSourceId string `json:"MediaSourceId"`
	PlaySessionId string `json:"PlaySessionId"`
	PositionTicks int64  `json:"PositionTicks"`
	DurationTicks *int64 `json:"DurationTicks,omitempty"`
	IsPaused      bool   `json:"IsPaused"`
	DeviceId      string `json:"DeviceId"`
}

// SessionsPlaying handles POST /sessions/playing.
func (h *PlaybackHandlers) SessionsPlaying(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req sessionReportRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.PlaySessionId != "" {
		now := models.UTCNow()
		leaseUntil := time.Now().UTC().Add(120 * time.Second).Format(time.RFC3339)
		_ = h.database.ExecWrite(r.Context(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(), `
				UPDATE play_sessions
				SET state = 'active', position_ticks = ?, lease_until = ?, updated_at = ?
				WHERE id = ? AND user_id = ?;
			`, req.PositionTicks, leaseUntil, now, req.PlaySessionId, user.ID)
			return err
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// SessionsProgress handles POST /sessions/playing/progress.
func (h *PlaybackHandlers) SessionsProgress(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req sessionReportRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.PlaySessionId != "" {
		now := models.UTCNow()
		leaseUntil := time.Now().UTC().Add(120 * time.Second).Format(time.RFC3339)
		// Extend lease
		_ = h.database.ExecWrite(r.Context(), func(tx *sql.Tx) error {
			_, err := tx.ExecContext(r.Context(), `
				UPDATE play_sessions
				SET lease_until = ?, updated_at = ?
				WHERE id = ? AND user_id = ?;
			`, leaseUntil, now, req.PlaySessionId, user.ID)
			return err
		})

		_ = h.progressRepo.UpdatePlayProgress(r.Context(), req.PlaySessionId, req.PositionTicks, req.DurationTicks, req.IsPaused)
	}

	w.WriteHeader(http.StatusNoContent)
}

// SessionsStopped handles POST /sessions/playing/stopped.
func (h *PlaybackHandlers) SessionsStopped(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	var req sessionReportRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.PlaySessionId != "" {
		// Update progress first
		_ = h.progressRepo.UpdatePlayProgress(r.Context(), req.PlaySessionId, req.PositionTicks, req.DurationTicks, false)
		// Close session idempotently
		_ = h.progressRepo.ClosePlaySession(r.Context(), req.PlaySessionId)
	}

	w.WriteHeader(http.StatusNoContent)
}
