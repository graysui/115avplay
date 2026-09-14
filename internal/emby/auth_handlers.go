package emby

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"mediavault/internal/db"
	"mediavault/internal/models"

	"github.com/google/uuid"
)

type AuthHandlers struct {
	serverID string
	baseURL  string
	userRepo *db.UserRepo
}

func NewAuthHandlers(serverID, baseURL string, userRepo *db.UserRepo) *AuthHandlers {
	return &AuthHandlers{
		serverID: serverID,
		baseURL:  baseURL,
		userRepo: userRepo,
	}
}

// GetPublicSystemInfo handles GET /system/info/public.
func (h *AuthHandlers) GetPublicSystemInfo(w http.ResponseWriter, r *http.Request) {
	info := PublicSystemInfo{
		ServerName:             "MediaVault",
		Version:                "4.8.0.0",
		Id:                     h.serverID,
		OperatingSystem:        "Linux",
		StartupWizardCompleted: true,
	}
	writeJSON(w, http.StatusOK, info)
}

// GetSystemInfo handles GET /system/info.
func (h *AuthHandlers) GetSystemInfo(w http.ResponseWriter, r *http.Request) {
	info := SystemInfo{
		ServerName:             "MediaVault",
		Version:                "4.8.0.0",
		Id:                     h.serverID,
		InternalAddress:        h.baseURL,
		LocalAddress:           h.baseURL,
		OperatingSystem:        "Linux",
		CanSelfRestart:         false,
		WebSocketPortNumber:    8096,
		CompletedInstallations: []string{},
		StartupWizardCompleted: true,
	}
	writeJSON(w, http.StatusOK, info)
}

// GetEndpoint handles GET /system/endpoint.
func (h *AuthHandlers) GetEndpoint(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"IsLocal":     true,
		"IsInNetwork": true,
	})
}

type loginRequest struct {
	Username string `json:"Username"`
	Pw       string `json:"Pw"`
}

// AuthenticateByName handles POST /users/authenticatebyname.
func (h *AuthHandlers) AuthenticateByName(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	req.Username = stringsTrim(req.Username)
	if req.Username == "" {
		http.Error(w, `{"error":"username required"}`, http.StatusBadRequest)
		return
	}

	user, err := h.userRepo.GetUserByUsername(r.Context(), req.Username)
	if err != nil || user == nil {
		http.Error(w, `{"error":"invalid username or password"}`, http.StatusUnauthorized)
		return
	}

	valid, err := db.VerifyPassword(user.PasswordHash, req.Pw)
	if err != nil || !valid {
		http.Error(w, `{"error":"invalid username or password"}`, http.StatusUnauthorized)
		return
	}

	if user.Enabled != 1 {
		http.Error(w, `{"error":"user account is disabled"}`, http.StatusForbidden)
		return
	}

	// Parse client info from X-Emby-Authorization or Authorization
	authHeader := r.Header.Get("X-Emby-Authorization")
	if authHeader == "" {
		authHeader = r.Header.Get("Authorization")
	}
	clientInfo := ParseEmbyAuthHeader(authHeader)

	deviceID := clientInfo.DeviceId
	if deviceID == "" {
		deviceID = "dev_" + uuid.New().String()[:8]
	}
	deviceName := clientInfo.Device
	if deviceName == "" {
		deviceName = "Generic Device"
	}
	clientName := clientInfo.Client
	if clientName == "" {
		clientName = "Emby Client"
	}

	// Generate secure random plain token (32 bytes = 64 hex characters)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		http.Error(w, `{"error":"failed to generate token"}`, http.StatusInternalServerError)
		return
	}
	plainToken := hex.EncodeToString(tokenBytes)

	// Hash token for database storage
	hasher := sha256.New()
	hasher.Write([]byte(plainToken))
	tokenHash := hex.EncodeToString(hasher.Sum(nil))

	nowTime := time.Now().UTC()
	now := models.UTCNow()
	expiresAt := nowTime.Add(90 * 24 * time.Hour).Format(time.RFC3339)
	sessionID := "sess_auth_" + uuid.New().String()[:8]

	session := &models.AuthSession{
		ID:         sessionID,
		UserID:     user.ID,
		TokenHash:  tokenHash,
		Audience:   "emby",
		DeviceID:   deviceID,
		DeviceName: &deviceName,
		ClientName: &clientName,
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
	}

	if err := h.userRepo.CreateSession(r.Context(), session); err != nil {
		http.Error(w, `{"error":"failed to create session"}`, http.StatusInternalServerError)
		return
	}

	resp := AuthResponse{
		User:        buildUserDTO(user, h.serverID),
		AccessToken: plainToken,
		ServerId:    h.serverID,
		SessionInfo: AuthSessionInfo{
			Id:                 sessionID,
			UserId:             user.ID,
			UserName:           user.Username,
			DeviceId:           deviceID,
			DeviceName:         deviceName,
			Client:             clientName,
			ApplicationVersion: clientInfo.Version,
		},
	}

	writeJSON(w, http.StatusOK, resp)
}

// GetMe handles GET /users/me.
func (h *AuthHandlers) GetMe(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, buildUserDTO(user, h.serverID))
}

// GetUserByID handles GET /users/{uid}.
func (h *AuthHandlers) GetUserByID(w http.ResponseWriter, r *http.Request, uid string) {
	user := UserFromContext(r.Context())
	if user == nil {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if uid != user.ID {
		http.Error(w, `{"error":"forbidden: cannot access another user's profile"}`, http.StatusForbidden)
		return
	}
	writeJSON(w, http.StatusOK, buildUserDTO(user, h.serverID))
}

// PostCapabilities handles POST /sessions/capabilities and /sessions/capabilities/full.
func (h *AuthHandlers) PostCapabilities(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// PostLogout handles POST /sessions/logout.
func (h *AuthHandlers) PostLogout(w http.ResponseWriter, r *http.Request) {
	session := SessionFromContext(r.Context())
	if session != nil {
		_ = h.userRepo.DeleteSession(r.Context(), session.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func buildUserDTO(user *models.User, serverID string) UserDTO {
	return UserDTO{
		Name:                      user.Username,
		ServerId:                  serverID,
		Id:                        user.ID,
		HasPassword:               true,
		HasConfiguredPassword:     true,
		HasConfiguredEasyPassword: false,
		EnableAutoLogin:           false,
		Configuration: &UserConfiguration{
			PlayDefaultAudioTrack:      true,
			DisplayMissingEpisodes:     false,
			GroupedFolders:             []string{},
			SubtitleMode:               "Default",
			DisplayCollectionsView:     true,
			EnableLocalPassword:        false,
			OrderedViews:               []string{},
			LatestItemsExcludes:        []string{},
			MyMediaExcludes:            []string{},
			HidePlayedInLatest:         false,
			RememberAudioSelections:    true,
			RememberSubtitleSelections: true,
			EnableNextEpisodeAutoPlay:  true,
		},
		Policy: UserPolicy{
			IsAdministrator:                  user.IsAdmin == 1,
			IsHidden:                         false,
			IsDisabled:                       user.Enabled != 1,
			EnableSharedDevice:               true,
			EnableRemoteControlOfOtherUsers:  false,
			EnableLiveTvManagement:           false,
			EnableLiveTvAccess:               true,
			EnableMediaPlayback:              true,
			EnableAudioPlaybackTranscoding:   false,
			EnableVideoPlaybackTranscoding:   false,
			EnablePlaybackRemuxing:           false,
			ForceRemoteSourceTranscoding:     false,
			EnableContentDeleting:            false,
			EnableContentDownloading:         false,
			EnableSyncTranscoding:            false,
		},
	}
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func stringsTrim(s string) string {
	return strings.TrimSpace(s)
}
