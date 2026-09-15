package emby

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"mediavault/internal/client115"
	"mediavault/internal/db"
	"mediavault/internal/models"
	"mediavault/internal/services"
)

func setupTestEmbyEnv(t *testing.T) (*Server, *db.DB, *models.User, string) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "test.db")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = database.Close()
	})

	ctx := context.Background()
	if _, err := db.InitEmptyDatabase(ctx, database); err != nil {
		t.Fatalf("failed to init db schema: %v", err)
	}

	// Mock 115 and image server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cover.jpg" {
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("fake-jpeg-bytes"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/open/ufile/downurl" {
			_ = json.NewEncoder(w).Encode(client115.BaseResponse{
				State: true,
				Code:  0,
				Data: json.RawMessage(`{
					"pick_1001": {
						"url": "https://cdn.115.com/download/SSIS-123.mp4?sign=valid",
						"file_size": 3000000000,
						"file_name": "SSIS-123.mp4",
						"file_id": "file_1001",
						"pick_code": "pick_1001"
					}
				}`),
			})
			return
		}
		_ = json.NewEncoder(w).Encode(client115.BaseResponse{State: true, Code: 0})
	}))
	t.Cleanup(mockServer.Close)

	userRepo := db.NewUserRepo(database)
	adminUser, err := userRepo.EnsureAdminUser(ctx, "admin", "password123", false)
	if err != nil {
		t.Fatalf("failed to ensure admin user: %v", err)
	}

	movieRepo := db.NewMovieRepo(database)
	magnetRepo := db.NewMagnetRepo(database)
	assetRepo := db.NewAssetRepo(database)
	progressRepo := db.NewProgressRepo(database)
	libraryRepo := db.NewLibraryRepo(database)
	jobRepo := db.NewJobRepo(database)

	// Seed 115 binding
	_ = assetRepo.UpsertBinding(ctx, &models.CloudBinding{
		ID:               "binding_test_115",
		Provider:         "115",
		ProviderUserID:   "user_115",
		Enabled:          1,
		SecretSettingKey: "provider_tokens:binding_test_115",
	})

	// Seed sample movie
	titleZh := "顶级作品 SSIS-123"
	descZh := "这是测试影片的详细中文简介"
	relDate := "2024-05-20"
	score := 4.5
	rTicks := int64(72000000000) // 120 minutes in 100ns ticks
	cover := mockServer.URL + "/cover.jpg"

	_ = movieRepo.UpsertMovie(ctx, &models.Movie{
		Code:          "SSIS-123",
		Title:         "SSIS-123 Original",
		OfficialTitle: titleZh,
		TitleZh:       &titleZh,
		DescriptionZh: &descZh,
		Category:      "亚洲有码",
		ReleaseDate:   &relDate,
		Score:         &score,
		RuntimeTicks:  &rTicks,
		CoverURL:      &cover,
		Actors:        `["三上悠亚", "河北彩花"]`,
		Tags:          `["巨乳", "单体作品"]`,
		FirstSeenAt:   models.UTCNow(),
	})

	// Seed permanent magnet + asset
	resKey := "115:binding_test_115:file_1001"
	mTitle := "SSIS-123 Permanent HD"
	_ = magnetRepo.UpsertMagnet(ctx, &models.Magnet{
		InfoHash:      resKey,
		MovieCode:     "SSIS-123",
		ResourceKind:  "existing",
		MagnetURL:     resKey,
		Title:         &mTitle,
		SizeBytes:     3000000000,
		QualityLabel:  "1080P",
		HasChineseSub: 1,
		Enabled:       1,
	})

	fid := "file_1001"
	pc := "pick_1001"
	_ = assetRepo.UpsertAsset(ctx, &models.CloudAsset{
		ID:          "ast_perm_1",
		BindingID:   "binding_test_115",
		ResourceKey: &resKey,
		SourceType:  "permanent",
		State:       "ready",
		FileID:      &fid,
		PickCode:    &pc,
		SizeBytes:   3000000000,
	})

	c115Client, _ := client115.NewClient(client115.ClientConfig{BaseURL: mockServer.URL})
	transferManager := services.NewTransferManager(database, assetRepo, magnetRepo, jobRepo, c115Client, "cid_temp", 7, nil)
	resolver := services.NewResolver(database, assetRepo, magnetRepo, transferManager, c115Client, 100, 120, nil)

	cacheDir := filepath.Join(tempDir, "imgcache")
	server, err := NewServer(
		database, movieRepo, magnetRepo, assetRepo, userRepo,
		progressRepo, libraryRepo, resolver, cacheDir, "http://127.0.0.1:8096", nil,
	)
	if err != nil {
		t.Fatalf("failed to create emby server: %v", err)
	}

	return server, database, adminUser, "password123"
}

func TestSystemHandshake(t *testing.T) {
	server, _, _, _ := setupTestEmbyEnv(t)

	// 1. GET /system/info/public
	req := httptest.NewRequest("GET", "/system/info/public", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var pubInfo PublicSystemInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &pubInfo); err != nil {
		t.Fatalf("unmarshal public info: %v", err)
	}
	if pubInfo.ServerName != "MediaVault" || pubInfo.Version != "4.8.0.0" || pubInfo.Id == "" {
		t.Errorf("unexpected public info: %+v", pubInfo)
	}

	// 2. Case-insensitive route with /emby prefix: /emby/System/Info/Public
	req2 := httptest.NewRequest("GET", "/emby/System/Info/Public", nil)
	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200 for /emby/System/Info/Public, got %d", rec2.Code)
	}

	// 3. GET /system/endpoint
	req3 := httptest.NewRequest("GET", "/system/endpoint", nil)
	rec3 := httptest.NewRecorder()
	server.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("expected 200 for /system/endpoint, got %d", rec3.Code)
	}
}

func TestAuthAndTokens(t *testing.T) {
	server, _, _, plainPassword := setupTestEmbyEnv(t)

	// 1. POST /users/authenticatebyname with correct credentials
	loginBody, _ := json.Marshal(map[string]string{
		"Username": "admin",
		"Pw":       plainPassword,
	})
	req := httptest.NewRequest("POST", "/users/authenticatebyname", bytes.NewReader(loginBody))
	req.Header.Set("X-Emby-Authorization", `MediaBrowser Client="Infuse", Device="Apple TV", DeviceId="DEV123", Version="7.5"`)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on login, got %d: %s", rec.Code, rec.Body.String())
	}

	var authResp AuthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &authResp); err != nil {
		t.Fatalf("unmarshal login resp: %v", err)
	}

	if authResp.AccessToken == "" || authResp.User.Name != "admin" || !authResp.User.Policy.IsAdministrator {
		t.Fatalf("invalid auth response: %+v", authResp)
	}
	token := authResp.AccessToken
	adminID := authResp.User.Id

	// 2. POST /users/authenticatebyname with wrong password -> 401
	badBody, _ := json.Marshal(map[string]string{
		"Username": "admin",
		"Pw":       "wrong_password",
	})
	reqBad := httptest.NewRequest("POST", "/users/authenticatebyname", bytes.NewReader(badBody))
	recBad := httptest.NewRecorder()
	server.ServeHTTP(recBad, reqBad)
	if recBad.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on bad password, got %d", recBad.Code)
	}

	// 3. Authenticated GET /users/me using X-Emby-Token
	reqMe := httptest.NewRequest("GET", "/users/me", nil)
	reqMe.Header.Set("X-Emby-Token", token)
	recMe := httptest.NewRecorder()
	server.ServeHTTP(recMe, reqMe)
	if recMe.Code != http.StatusOK {
		t.Errorf("expected 200 on /users/me with X-Emby-Token, got %d", recMe.Code)
	}

	// 4. Authenticated GET /system/info using api_key query param
	reqSys := httptest.NewRequest("GET", "/system/info?api_key="+token, nil)
	recSys := httptest.NewRecorder()
	server.ServeHTTP(recSys, reqSys)
	if recSys.Code != http.StatusOK {
		t.Errorf("expected 200 on /system/info with api_key, got %d", recSys.Code)
	}

	// 5. Conflicting tokens -> 401
	reqConflict := httptest.NewRequest("GET", "/users/me?api_key=wrong_token", nil)
	reqConflict.Header.Set("X-Emby-Token", token)
	recConflict := httptest.NewRecorder()
	server.ServeHTTP(recConflict, reqConflict)
	if recConflict.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 on conflicting tokens, got %d", recConflict.Code)
	}

	// 6. User isolation check: GET /users/other_user_id -> 403
	reqOther := httptest.NewRequest("GET", "/users/usr_other_123", nil)
	reqOther.Header.Set("X-Emby-Token", token)
	recOther := httptest.NewRecorder()
	server.ServeHTTP(recOther, reqOther)
	if recOther.Code != http.StatusForbidden {
		t.Errorf("expected 403 accessing other user, got %d", recOther.Code)
	}

	// GET own profile /users/{uid} -> 200
	reqSelf := httptest.NewRequest("GET", "/users/"+adminID, nil)
	reqSelf.Header.Set("X-Emby-Token", token)
	recSelf := httptest.NewRecorder()
	server.ServeHTTP(recSelf, reqSelf)
	if recSelf.Code != http.StatusOK {
		t.Errorf("expected 200 accessing own user profile, got %d", recSelf.Code)
	}

	// 7. POST /sessions/capabilities -> 204
	reqCap := httptest.NewRequest("POST", "/sessions/capabilities", nil)
	reqCap.Header.Set("X-Emby-Token", token)
	recCap := httptest.NewRecorder()
	server.ServeHTTP(recCap, reqCap)
	if recCap.Code != http.StatusNoContent {
		t.Errorf("expected 204 on /sessions/capabilities, got %d", recCap.Code)
	}

	// 8. POST /sessions/logout -> 204; subsequent request -> 401
	reqLogout := httptest.NewRequest("POST", "/sessions/logout", nil)
	reqLogout.Header.Set("X-Emby-Token", token)
	recLogout := httptest.NewRecorder()
	server.ServeHTTP(recLogout, reqLogout)
	if recLogout.Code != http.StatusNoContent {
		t.Errorf("expected 204 on logout, got %d", recLogout.Code)
	}

	recMeAfter := httptest.NewRecorder()
	server.ServeHTTP(recMeAfter, reqMe)
	if recMeAfter.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 after logout, got %d", recMeAfter.Code)
	}
}

func TestViewsAndMediaFolders(t *testing.T) {
	server, _, adminUser, plainPassword := setupTestEmbyEnv(t)

	// Login to obtain token
	token := getTestToken(t, server, plainPassword)

	// GET /library/mediafolders
	req := httptest.NewRequest("GET", "/library/mediafolders", nil)
	req.Header.Set("X-Emby-Token", token)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /library/mediafolders, got %d", rec.Code)
	}

	var resp ItemsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal views: %v", err)
	}

	if resp.TotalRecordCount != 9 || len(resp.Items) != 9 {
		t.Errorf("expected 9 fixed libraries (3 ranking + 6 category), got %d", resp.TotalRecordCount)
	}

	// GET /users/{uid}/views
	reqViews := httptest.NewRequest("GET", "/users/"+adminUser.ID+"/views", nil)
	reqViews.Header.Set("X-Emby-Token", token)
	recViews := httptest.NewRecorder()
	server.ServeHTTP(recViews, reqViews)
	if recViews.Code != http.StatusOK {
		t.Fatalf("expected 200 on /users/{uid}/views, got %d", recViews.Code)
	}
}

func TestItemsAndPlaybackFlow(t *testing.T) {
	server, _, _, plainPassword := setupTestEmbyEnv(t)
	token := getTestToken(t, server, plainPassword)

	// 1. GET /items?ParentId=lib_censored&SortBy=DateCreated&SortOrder=Descending
	reqItems := httptest.NewRequest("GET", "/items?ParentId=lib_censored&SortBy=DateCreated&SortOrder=Descending", nil)
	reqItems.Header.Set("X-Emby-Token", token)
	recItems := httptest.NewRecorder()
	server.ServeHTTP(recItems, reqItems)

	if recItems.Code != http.StatusOK {
		t.Fatalf("expected 200 on /items, got %d: %s", recItems.Code, recItems.Body.String())
	}

	var itemsResp ItemsResponse
	if err := json.Unmarshal(recItems.Body.Bytes(), &itemsResp); err != nil {
		t.Fatalf("unmarshal items: %v", err)
	}
	if itemsResp.TotalRecordCount != 1 || len(itemsResp.Items) != 1 {
		t.Fatalf("expected 1 item, got %d", itemsResp.TotalRecordCount)
	}

	item := itemsResp.Items[0]
	expectedItemID := EncodeItemID("SSIS-123")
	if item.Id != expectedItemID {
		t.Errorf("expected item id %s, got %s", expectedItemID, item.Id)
	}

	// 2. GET /items/{id} detail
	reqDetail := httptest.NewRequest("GET", "/items/"+expectedItemID, nil)
	reqDetail.Header.Set("X-Emby-Token", token)
	recDetail := httptest.NewRecorder()
	server.ServeHTTP(recDetail, reqDetail)

	if recDetail.Code != http.StatusOK {
		t.Fatalf("expected 200 on /items/{id}, got %d", recDetail.Code)
	}

	var detail ItemDTO
	if err := json.Unmarshal(recDetail.Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if len(detail.MediaSources) != 1 {
		t.Fatalf("expected 1 media source, got %d", len(detail.MediaSources))
	}
	source := detail.MediaSources[0]
	if !source.SupportsDirectPlay {
		t.Errorf("expected source SupportsDirectPlay to be true")
	}

	// 3. POST /items/{id}/playbackinfo
	reqPB := httptest.NewRequest("POST", "/items/"+expectedItemID+"/playbackinfo", nil)
	reqPB.Header.Set("X-Emby-Token", token)
	recPB := httptest.NewRecorder()
	server.ServeHTTP(recPB, reqPB)

	if recPB.Code != http.StatusOK {
		t.Fatalf("expected 200 on playbackinfo, got %d", recPB.Code)
	}

	var pbResp PlaybackInfoResponse
	if err := json.Unmarshal(recPB.Body.Bytes(), &pbResp); err != nil {
		t.Fatalf("unmarshal playbackinfo: %v", err)
	}
	if pbResp.PlaySessionId == "" || len(pbResp.MediaSources) != 1 {
		t.Fatalf("unexpected playbackinfo resp: %+v", pbResp)
	}
	playSessionID := pbResp.PlaySessionId

	// 4. GET /videos/{id}/stream -> 302 Found redirect to CDN URL
	reqStream := httptest.NewRequest("GET", "/videos/"+expectedItemID+"/stream", nil)
	reqStream.Header.Set("X-Emby-Token", token)
	recStream := httptest.NewRecorder()
	server.ServeHTTP(recStream, reqStream)

	if recStream.Code != http.StatusFound {
		t.Fatalf("expected 302 on stream, got %d", recStream.Code)
	}
	location := recStream.Header().Get("Location")
	if location != "https://cdn.115.com/download/SSIS-123.mp4?sign=valid" {
		t.Errorf("unexpected redirect location: %s", location)
	}
	if recStream.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("expected Cache-Control no-store")
	}

	// 5. HEAD /videos/{id}/stream -> 302 without body
	reqHead := httptest.NewRequest("HEAD", "/videos/"+expectedItemID+"/stream", nil)
	reqHead.Header.Set("X-Emby-Token", token)
	recHead := httptest.NewRecorder()
	server.ServeHTTP(recHead, reqHead)
	if recHead.Code != http.StatusFound {
		t.Errorf("expected 302 on HEAD stream, got %d", recHead.Code)
	}

	// 6. Progress flow: playing -> progress -> stopped
	playBody, _ := json.Marshal(map[string]interface{}{
		"ItemId":        expectedItemID,
		"PlaySessionId": playSessionID,
		"PositionTicks": int64(1000000000), // 100s
	})
	reqPlaying := httptest.NewRequest("POST", "/sessions/playing", bytes.NewReader(playBody))
	reqPlaying.Header.Set("X-Emby-Token", token)
	recPlaying := httptest.NewRecorder()
	server.ServeHTTP(recPlaying, reqPlaying)
	if recPlaying.Code != http.StatusNoContent {
		t.Errorf("expected 204 on playing, got %d", recPlaying.Code)
	}

	progBody, _ := json.Marshal(map[string]interface{}{
		"ItemId":        expectedItemID,
		"PlaySessionId": playSessionID,
		"PositionTicks": int64(68000000000), // > 90% of 72000000000
		"DurationTicks": int64(72000000000),
		"IsPaused":      false,
	})
	reqProg := httptest.NewRequest("POST", "/sessions/playing/progress", bytes.NewReader(progBody))
	reqProg.Header.Set("X-Emby-Token", token)
	recProg := httptest.NewRecorder()
	server.ServeHTTP(recProg, reqProg)
	if recProg.Code != http.StatusNoContent {
		t.Errorf("expected 204 on progress, got %d", recProg.Code)
	}

	// Stopped
	stopBody, _ := json.Marshal(map[string]interface{}{
		"ItemId":        expectedItemID,
		"PlaySessionId": playSessionID,
		"PositionTicks": int64(68000000000),
	})
	reqStop := httptest.NewRequest("POST", "/sessions/playing/stopped", bytes.NewReader(stopBody))
	reqStop.Header.Set("X-Emby-Token", token)
	recStop := httptest.NewRecorder()
	server.ServeHTTP(recStop, reqStop)
	if recStop.Code != http.StatusNoContent {
		t.Errorf("expected 204 on stopped, got %d", recStop.Code)
	}

	// Repeat stopped: idempotent, must not error
	recStop2 := httptest.NewRecorder()
	reqStop2 := httptest.NewRequest("POST", "/sessions/playing/stopped", bytes.NewReader(stopBody))
	reqStop2.Header.Set("X-Emby-Token", token)
	server.ServeHTTP(recStop2, reqStop2)
	if recStop2.Code != http.StatusNoContent {
		t.Errorf("expected 204 on repeated stopped, got %d", recStop2.Code)
	}

	// 7. GET /items/counts
	reqCounts := httptest.NewRequest("GET", "/items/counts", nil)
	reqCounts.Header.Set("X-Emby-Token", token)
	recCounts := httptest.NewRecorder()
	server.ServeHTTP(recCounts, reqCounts)
	if recCounts.Code != http.StatusOK {
		t.Errorf("expected 200 on /items/counts, got %d", recCounts.Code)
	}

	// 8. GET /shows/nextup
	reqNext := httptest.NewRequest("GET", "/shows/nextup", nil)
	reqNext.Header.Set("X-Emby-Token", token)
	recNext := httptest.NewRecorder()
	server.ServeHTTP(recNext, reqNext)
	if recNext.Code != http.StatusOK {
		t.Errorf("expected 200 on /shows/nextup, got %d", recNext.Code)
	}
}

func TestImagePlaceholderAndETag(t *testing.T) {
	server, _, _, plainPassword := setupTestEmbyEnv(t)
	token := getTestToken(t, server, plainPassword)

	itemID := EncodeItemID("SSIS-123")
	req := httptest.NewRequest("GET", "/items/"+itemID+"/images/Backdrop", nil)
	req.Header.Set("X-Emby-Token", token)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on image, got %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Errorf("expected ETag header on image response")
	}

	// Second request with If-None-Match
	req2 := httptest.NewRequest("GET", "/items/"+itemID+"/images/Backdrop", nil)
	req2.Header.Set("X-Emby-Token", token)
	req2.Header.Set("If-None-Match", etag)
	rec2 := httptest.NewRecorder()
	server.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotModified {
		t.Errorf("expected 304 on matching ETag, got %d", rec2.Code)
	}
}

func TestUserDataAndCollections(t *testing.T) {
	server, _, adminUser, plainPassword := setupTestEmbyEnv(t)
	token := getTestToken(t, server, plainPassword)

	itemID := EncodeItemID("SSIS-123")

	// 1. Favorite item: POST /users/{uid}/favoriteitems/{id}
	reqFav := httptest.NewRequest("POST", "/users/"+adminUser.ID+"/favoriteitems/"+itemID, nil)
	reqFav.Header.Set("X-Emby-Token", token)
	recFav := httptest.NewRecorder()
	server.ServeHTTP(recFav, reqFav)
	if recFav.Code != http.StatusOK {
		t.Fatalf("expected 200 on favorite, got %d", recFav.Code)
	}

	// 2. Unfavorite item: DELETE /users/{uid}/favoriteitems/{id}
	reqUnfav := httptest.NewRequest("DELETE", "/users/"+adminUser.ID+"/favoriteitems/"+itemID, nil)
	reqUnfav.Header.Set("X-Emby-Token", token)
	recUnfav := httptest.NewRecorder()
	server.ServeHTTP(recUnfav, reqUnfav)
	if recUnfav.Code != http.StatusOK {
		t.Fatalf("expected 200 on unfavorite, got %d", recUnfav.Code)
	}

	// 3. Mark played: POST /users/{uid}/playeditems/{id}
	reqPlay := httptest.NewRequest("POST", "/users/"+adminUser.ID+"/playeditems/"+itemID, nil)
	reqPlay.Header.Set("X-Emby-Token", token)
	recPlay := httptest.NewRecorder()
	server.ServeHTTP(recPlay, reqPlay)
	if recPlay.Code != http.StatusOK {
		t.Fatalf("expected 200 on mark played, got %d", recPlay.Code)
	}

	// 4. Mark unplayed: DELETE /users/{uid}/playeditems/{id}
	reqUnplay := httptest.NewRequest("DELETE", "/users/"+adminUser.ID+"/playeditems/"+itemID, nil)
	reqUnplay.Header.Set("X-Emby-Token", token)
	recUnplay := httptest.NewRecorder()
	server.ServeHTTP(recUnplay, reqUnplay)
	if recUnplay.Code != http.StatusOK {
		t.Fatalf("expected 200 on unmark played, got %d", recUnplay.Code)
	}

	// 5. GET /items/latest -> returns []ItemDTO
	reqLatest := httptest.NewRequest("GET", "/items/latest", nil)
	reqLatest.Header.Set("X-Emby-Token", token)
	recLatest := httptest.NewRecorder()
	server.ServeHTTP(recLatest, reqLatest)
	if recLatest.Code != http.StatusOK {
		t.Fatalf("expected 200 on latest, got %d", recLatest.Code)
	}
	var latestList []ItemDTO
	if err := json.Unmarshal(recLatest.Body.Bytes(), &latestList); err != nil {
		t.Fatalf("unmarshal latest items: %v", err)
	}
	if len(latestList) != 1 {
		t.Errorf("expected 1 latest item, got %d", len(latestList))
	}

	// 6. GET /items/resume -> returns ItemsResponse
	reqResume := httptest.NewRequest("GET", "/items/resume", nil)
	reqResume.Header.Set("X-Emby-Token", token)
	recResume := httptest.NewRecorder()
	server.ServeHTTP(recResume, reqResume)
	if recResume.Code != http.StatusOK {
		t.Fatalf("expected 200 on resume, got %d", recResume.Code)
	}
	var resumeResp ItemsResponse
	if err := json.Unmarshal(recResume.Body.Bytes(), &resumeResp); err != nil {
		t.Fatalf("unmarshal resume: %v", err)
	}
}

func TestSearchAndSorting(t *testing.T) {
	server, _, _, plainPassword := setupTestEmbyEnv(t)
	token := getTestToken(t, server, plainPassword)

	// 1. Search by keyword
	reqSearch := httptest.NewRequest("GET", "/items?SearchTerm=SSIS", nil)
	reqSearch.Header.Set("X-Emby-Token", token)
	recSearch := httptest.NewRecorder()
	server.ServeHTTP(recSearch, reqSearch)
	if recSearch.Code != http.StatusOK {
		t.Fatalf("expected 200 on search, got %d", recSearch.Code)
	}
	var searchResp ItemsResponse
	_ = json.Unmarshal(recSearch.Body.Bytes(), &searchResp)
	if searchResp.TotalRecordCount != 1 {
		t.Errorf("expected 1 search result, got %d", searchResp.TotalRecordCount)
	}

	// 2. Search by non-existent term
	reqNoMatch := httptest.NewRequest("GET", "/items?SearchTerm=NONEXISTENT_XYZ", nil)
	reqNoMatch.Header.Set("X-Emby-Token", token)
	recNoMatch := httptest.NewRecorder()
	server.ServeHTTP(recNoMatch, reqNoMatch)
	var noMatchResp ItemsResponse
	_ = json.Unmarshal(recNoMatch.Body.Bytes(), &noMatchResp)
	if noMatchResp.TotalRecordCount != 0 {
		t.Errorf("expected 0 results, got %d", noMatchResp.TotalRecordCount)
	}

	// 3. Valid SortBy: SortName, PremiereDate, plus fields sent by VidHub/Infuse
	for _, sortField := range []string{"SortName", "PremiereDate", "Random", "DateCreated,SortName", "CommunityRating"} {
		reqSort := httptest.NewRequest("GET", "/items?SortBy="+sortField+"&SortOrder=Ascending", nil)
		reqSort.Header.Set("X-Emby-Token", token)
		recSort := httptest.NewRecorder()
		server.ServeHTTP(recSort, reqSort)
		if recSort.Code != http.StatusOK {
			t.Errorf("expected 200 for SortBy=%s, got %d", sortField, recSort.Code)
		}
	}

	// 4. Unknown SortBy falls back to the default order (no 400, no injection).
	reqBadSort := httptest.NewRequest("GET", "/items?SortBy=MaliciousSQLInjection", nil)
	reqBadSort.Header.Set("X-Emby-Token", token)
	recBadSort := httptest.NewRecorder()
	server.ServeHTTP(recBadSort, reqBadSort)
	if recBadSort.Code != http.StatusOK {
		t.Errorf("expected 200 (fallback) on unknown SortBy, got %d", recBadSort.Code)
	}
}

func getTestToken(t *testing.T, server *Server, plainPassword string) string {
	loginBody, _ := json.Marshal(map[string]string{
		"Username": "admin",
		"Pw":       plainPassword,
	})
	req := httptest.NewRequest("POST", "/users/authenticatebyname", bytes.NewReader(loginBody))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d", rec.Code)
	}

	var authResp AuthResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &authResp)
	return authResp.AccessToken
}
