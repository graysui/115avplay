package admin_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mediavault/internal/api/admin"
	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/logger"
	"mediavault/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)


func setupTestServer(t *testing.T) (*gin.Engine, *db.DB, *config.AppConfig, string) {
	gin.SetMode(gin.TestMode)
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})

	ctx := context.Background()
	_, err = db.InitEmptyDatabase(ctx, database)
	if err != nil {
		t.Fatalf("failed to init database: %v", err)
	}

	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i + 1)
	}
	masterKey, _ := config.NewMasterKeyFromBytes(keyBytes)

	appConfig := &config.AppConfig{
		DataDir:       tmpDir,
		MasterKey:     masterKey,
		MasterKeyFile: filepath.Join(tmpDir, "master.key"),
		EnvOverrides: map[string]string{
			"stream_wait_ms": "5000",
		},
	}

	userRepo := db.NewUserRepo(database)
	settingsRepo := db.NewSettingsRepo(database)
	movieRepo := db.NewMovieRepo(database)
	magnetRepo := db.NewMagnetRepo(database)
	assetRepo := db.NewAssetRepo(database)
	libraryRepo := db.NewLibraryRepo(database)
	jobRepo := db.NewJobRepo(database)

	// Create admin user
	_, err = userRepo.EnsureAdminUser(ctx, "admin", "admin123456", false)
	if err != nil {
		t.Fatalf("failed to ensure admin user: %v", err)
	}

	// Create regular user
	regHash, _ := db.HashPassword("user123456")
	_ = userRepo.CreateUser(ctx, &models.User{
		ID:           "usr_regular",
		Username:     "regular",
		PasswordHash: regHash,
		IsAdmin:      0,
		Enabled:      1,
	})

	engine := gin.New()
	rg := engine.Group("/api/v1")
	admin.RegisterAdminRoutes(rg, userRepo, settingsRepo, movieRepo, magnetRepo, assetRepo, libraryRepo, jobRepo, database, appConfig, nil, nil)

	return engine, database, appConfig, "admin123456"
}

func adminLogin(t *testing.T, r *gin.Engine, username, password string) (string, string) {
	loginBody, _ := json.Marshal(map[string]string{
		"username": username,
		"password": password,
	})
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("login failed with code %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Token     string `json:"token"`
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	return resp.Data.Token, resp.Data.CSRFToken
}

// T-701: Admin Auth Tests
func TestAdminAuth(t *testing.T) {
	r, database, _, _ := setupTestServer(t)

	// 1. Successful Admin Login
	token, csrf := adminLogin(t, r, "admin", "admin123456")
	if token == "" || csrf == "" {
		t.Fatalf("expected non-empty token and csrf")
	}

	// 2. Regular user cannot login to admin console
	loginBody, _ := json.Marshal(map[string]string{
		"username": "regular",
		"password": "user123456",
	})
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for regular user admin login, got %d", w.Code)
	}

	// 3. Emby session token is rejected by AdminAuthMiddleware
	userRepo := db.NewUserRepo(database)
	embyToken := "emby_secret_session_token_123"
	hasher := sha256.New()
	hasher.Write([]byte(embyToken))
	embyTokenHash := hex.EncodeToString(hasher.Sum(nil))
	_ = userRepo.CreateSession(context.Background(), &models.AuthSession{
		ID:        "sess_emby_test",
		UserID:    "usr_regular",
		TokenHash: embyTokenHash,
		Audience:  "emby",
		DeviceID:  "emby_client",
		CreatedAt: models.UTCNow(),
		ExpiresAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})

	req, _ = http.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+embyToken)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for emby audience token, got %d", w.Code)
	}

	// 4. Valid admin token can access /auth/me
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for admin me, got %d", w.Code)
	}

	// 5. CSRF check: POST with Cookie but mismatched CSRF token returns 403
	postReq, _ := http.NewRequest(http.MethodPost, "/api/v1/auth/password", bytes.NewReader([]byte(`{}`)))
	postReq.Header.Set("Content-Type", "application/json")
	postReq.AddCookie(&http.Cookie{Name: "mv_session", Value: token})
	postReq.AddCookie(&http.Cookie{Name: "mv_csrf", Value: csrf})
	postReq.Header.Set("X-CSRF-Token", "wrong_csrf_token")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, postReq)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for CSRF mismatch, got %d", w.Code)
	}

	// 6. Admin change password
	pwdBody, _ := json.Marshal(map[string]string{
		"old_password": "admin123456",
		"new_password": "newpassword123",
	})
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/auth/password", bytes.NewReader(pwdBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for change password, got %d: %s", w.Code, w.Body.String())
	}

	// 7. Verify login with new password
	newToken, _ := adminLogin(t, r, "admin", "newpassword123")
	if newToken == "" {
		t.Fatalf("expected login with new password to succeed")
	}

	// 8. Logout
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+newToken)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for logout, got %d", w.Code)
	}
}

// T-702: Stats API Tests
func TestStatsAPI(t *testing.T) {
	r, database, _, _ := setupTestServer(t)
	token, _ := adminLogin(t, r, "admin", "admin123456")

	// Insert test movies
	movieRepo := db.NewMovieRepo(database)
	title := "Test 1"
	titleZh := "测试影片"
	descZh := "描述内容"
	cover := "https://example.com/c.jpg"
	actors := `["Actor1"]`
	_ = movieRepo.UpsertMovie(context.Background(), &models.Movie{
		Code:          "TEST-001",
		Title:         title,
		TitleZh:       &titleZh,
		DescriptionZh: &descZh,
		CoverURL:      &cover,
		Actors:        actors,
		ScrapePolicy:  "auto",
		ScrapeStatus:  "success",
	})

	req, _ := http.NewRequest(http.MethodGet, "/api/v1/stats", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for stats, got %d", w.Code)
	}

	var resp struct {
		Data struct {
			Completeness struct {
				Complete int `json:"complete"`
			} `json:"completeness"`
			Media struct {
				TotalMovies int `json:"total_movies"`
			} `json:"media"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Media.TotalMovies != 1 {
		t.Fatalf("expected 1 total movie, got %d", resp.Data.Media.TotalMovies)
	}
	if resp.Data.Completeness.Complete != 1 {
		t.Fatalf("expected 1 complete movie, got %d", resp.Data.Completeness.Complete)
	}
}

// T-703: Settings API Tests (Revision, Secrets, Env Overrides)
func TestSettingsAPI(t *testing.T) {
	r, _, _, _ := setupTestServer(t)
	token, _ := adminLogin(t, r, "admin", "admin123456")

	// 1. GET Settings
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for get settings, got %d", w.Code)
	}

	var getResp struct {
		Data struct {
			Revision     int                    `json:"revision"`
			Values       map[string]string      `json:"values"`
			Secrets      map[string]interface{} `json:"secrets"`
			EnvOverrides map[string]string      `json:"env_overrides"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &getResp)
	rev := getResp.Data.Revision
	if getResp.Data.EnvOverrides["stream_wait_ms"] != "5000" {
		t.Fatalf("expected stream_wait_ms env override to be 5000")
	}

	// 2. Attempting to modify environment-overridden setting returns 400
	putBody, _ := json.Marshal(map[string]interface{}{
		"revision": rev,
		"values": map[string]string{
			"stream_wait_ms": "8000",
		},
	})
	req, _ = http.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for modifying env override, got %d", w.Code)
	}

	// 3. Optimistic concurrency check: outdated revision returns 409
	putBody, _ = json.Marshal(map[string]interface{}{
		"revision": rev + 99,
		"values": map[string]string{
			"cleanup_ttl_days": "14",
		},
	})
	req, _ = http.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict for stale revision, got %d", w.Code)
	}

	// 4. Update normal setting & secret replace
	putBody, _ = json.Marshal(map[string]interface{}{
		"revision": rev,
		"values": map[string]string{
			"cleanup_ttl_days": "14",
		},
		"secrets": map[string]interface{}{
			"alert_webhook": map[string]string{
				"action": "replace",
				"value":  "https://webhook.example.com/alert",
			},
		},
	})
	req, _ = http.NewRequest(http.MethodPut, "/api/v1/settings", bytes.NewReader(putBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid settings update, got %d: %s", w.Code, w.Body.String())
	}

	// 5. Verify GET shows secret configured=true without leaking plaintext
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var getResp2 struct {
		Data struct {
			Values  map[string]string `json:"values"`
			Secrets map[string]struct {
				Configured bool `json:"configured"`
			} `json:"secrets"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &getResp2)
	if getResp2.Data.Values["cleanup_ttl_days"] != "14" {
		t.Fatalf("expected cleanup_ttl_days to be 14, got %s", getResp2.Data.Values["cleanup_ttl_days"])
	}
	if !getResp2.Data.Secrets["alert_webhook"].Configured {
		t.Fatalf("expected alert_webhook to be configured")
	}
}

// T-704: Movies & Magnets API Tests (Manual Locks, Magnets, Soft Delete)
func TestMoviesAPI(t *testing.T) {
	r, database, _, _ := setupTestServer(t)
	token, _ := adminLogin(t, r, "admin", "admin123456")

	movieRepo := db.NewMovieRepo(database)
	_ = movieRepo.UpsertMovie(context.Background(), &models.Movie{
		Code:         "IPX-123",
		Title:        "Original Title",
		Category:     "censored",
		ScrapePolicy: "auto",
	})

	// 1. Get Movie
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/movies/IPX-123", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for get movie, got %d", w.Code)
	}

	// 2. Update Movie and check manual_fields lock
	newTitleZh := "自定义中文标题"
	newScore := 4.8
	updateBody, _ := json.Marshal(map[string]interface{}{
		"title_zh": newTitleZh,
		"score":    newScore,
	})
	req, _ = http.NewRequest(http.MethodPut, "/api/v1/movies/IPX-123", bytes.NewReader(updateBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for update movie, got %d: %s", w.Code, w.Body.String())
	}

	// Check updated movie from DB
	m, _ := movieRepo.GetMovie(context.Background(), "IPX-123")
	if m.TitleZh == nil || *m.TitleZh != newTitleZh {
		t.Fatalf("expected updated title_zh")
	}
	if !strings.Contains(m.ManualFields, "title_zh") || !strings.Contains(m.ManualFields, "score") {
		t.Fatalf("expected manual_fields to contain title_zh and score, got %s", m.ManualFields)
	}

	// 3. Add Magnet
	magBody, _ := json.Marshal(map[string]interface{}{
		"info_hash":       "abcdef1234567890abcdef1234567890abcdef12",
		"magnet_url":      "magnet:?xt=urn:btih:abcdef1234567890abcdef1234567890abcdef12",
		"title":           "Manual HD Chinese Sub",
		"size_bytes":      2147483648,
		"has_chinese_sub": 1,
		"is_preferred":    1,
	})
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/movies/IPX-123/magnets", bytes.NewReader(magBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for add magnet, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Soft delete movie
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/movies/IPX-123", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for delete movie, got %d", w.Code)
	}
	mAfter, _ := movieRepo.GetMovie(context.Background(), "IPX-123")
	if mAfter.DeletedAt == nil {
		t.Fatalf("expected deleted_at to be non-nil after soft delete")
	}
}

// T-706 & T-707: Tasks & Schedules API Tests
func TestTasksAndSchedulesAPI(t *testing.T) {
	r, _, _, _ := setupTestServer(t)
	token, _ := adminLogin(t, r, "admin", "admin123456")

	// 1. Trigger Task (sync30d)
	trigBody, _ := json.Marshal(map[string]interface{}{
		"kind": "sync30d",
	})
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/tasks/trigger", bytes.NewReader(trigBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for trigger task, got %d", w.Code)
	}

	var trigResp struct {
		Status string `json:"status"`
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &trigResp)
	if trigResp.Status != "queued" {
		t.Fatalf("expected queued status, got %s", trigResp.Status)
	}

	// 2. Duplicate trigger reuses active task
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/tasks/trigger", bytes.NewReader(trigBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 for duplicate trigger, got %d", w.Code)
	}
	var trigResp2 struct {
		Status string `json:"status"`
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &trigResp2)
	if trigResp2.Status != "reused_active" || trigResp2.TaskID != trigResp.TaskID {
		t.Fatalf("expected reused_active with matching task id, got status=%s, id=%s", trigResp2.Status, trigResp2.TaskID)
	}

	// 3. Cancel task
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/tasks/"+trigResp.TaskID+"/cancel", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for cancel task, got %d", w.Code)
	}

	// 4. Retry task
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/tasks/"+trigResp.TaskID+"/retry", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for retry task, got %d", w.Code)
	}

	// 5. Schedules CRUD
	schedBody, _ := json.Marshal(map[string]interface{}{
		"id":        "sched_test_sync",
		"kind":      "sync30d",
		"timezone":  "Asia/Shanghai",
		"rule_json": `{"type":"daily","hour":4,"minute":0}`,
		"enabled":   1,
	})
	req, _ = http.NewRequest(http.MethodPost, "/api/v1/schedules", bytes.NewReader(schedBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for create schedule, got %d: %s", w.Code, w.Body.String())
	}

	// List schedules
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/schedules", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for list schedules, got %d", w.Code)
	}
}

// T-712: Users API & Admin Protection Tests
func TestUsersAPI(t *testing.T) {
	r, _, _, _ := setupTestServer(t)
	token, _ := adminLogin(t, r, "admin", "admin123456")

	// 1. Create User
	createUserBody, _ := json.Marshal(map[string]interface{}{
		"username": "tester",
		"password": "testerpassword",
		"is_admin": 0,
	})
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader(createUserBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for create user, got %d: %s", w.Code, w.Body.String())
	}

	var createResp struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &createResp)
	newUserID := createResp.Data.ID

	// 2. List Users
	req, _ = http.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for list users, got %d", w.Code)
	}

	// 3. Admin Protection: Attempting to disable the only admin returns 400
	// First find admin user id
	var listResp struct {
		Data []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	var adminID string
	for _, u := range listResp.Data {
		if u.Username == "admin" {
			adminID = u.ID
			break
		}
	}

	disableBody, _ := json.Marshal(map[string]interface{}{
		"enabled": 0,
	})
	req, _ = http.NewRequest(http.MethodPut, "/api/v1/users/"+adminID, bytes.NewReader(disableBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for disabling last admin, got %d", w.Code)
	}

	// 4. Admin Protection: Attempting to delete the only admin returns 400
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/users/"+adminID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for deleting last admin, got %d", w.Code)
	}

	// 5. Delete regular user succeeds
	req, _ = http.NewRequest(http.MethodDelete, "/api/v1/users/"+newUserID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for delete regular user, got %d", w.Code)
	}
}

// T-708: WebSocket Log Stream Tests
func TestWebSocketLogs(t *testing.T) {
	r, _, _, _ := setupTestServer(t)
	token, _ := adminLogin(t, r, "admin", "admin123456")

	// Add entry to ring buffer with sensitive token
	if logger.GlobalRingBuffer != nil {
		logger.GlobalRingBuffer.Add(logger.LogEntry{
			Timestamp: time.Now().UTC(),
			Level:     "INFO",
			Message:   "User login successful bearer secret_token_value_abc",
			Attrs: map[string]interface{}{
				"token":  "super_secret_token",
				"job_id": "job_123",
			},
		})
	}

	srv := httptest.NewServer(r)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	wsURL := "ws://" + u.Host + "/api/v1/ws/logs?token=" + token

	header := make(http.Header)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("failed to dial websocket: %v, status: %v", err, resp)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg struct {
		Level   string                 `json:"level"`
		Message string                 `json:"message"`
		Attrs   map[string]interface{} `json:"attrs"`
	}
	err = conn.ReadJSON(&msg)
	if err != nil {
		t.Fatalf("failed to read log entry from websocket: %v", err)
	}

	// Verify desensitization
	if strings.Contains(msg.Message, "secret_token_value_abc") {
		t.Fatalf("sensitive token leaked in message: %s", msg.Message)
	}
	if msg.Attrs["token"] != "[REDACTED]" {
		t.Fatalf("sensitive token attribute not redacted: %v", msg.Attrs["token"])
	}
	if msg.Attrs["job_id"] != "job_123" {
		t.Fatalf("job_id should be preserved: %v", msg.Attrs["job_id"])
	}
}
