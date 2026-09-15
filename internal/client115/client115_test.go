package client115

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func setupMock115Server() (*httptest.Server, *int) {
	refreshCount := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		w.Header().Set("Content-Type", "application/json")

		switch path {
		case "/open/authDeviceCode":
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State: true,
				Code:  0,
				Data:  json.RawMessage(`{"device_code":"dev_123","user_code":"usr_456","qrcode_url":"https://115.com/qr","expires_in":1800}`),
			})

		case "/open/deviceCodeToToken":
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State: true,
				Code:  0,
				Data:  json.RawMessage(`{"access_token":"at_initial","refresh_token":"rt_initial","expires_in":7200,"user_id":"u_888"}`),
			})

		case "/open/refreshToken":
			refreshCount++
			time.Sleep(50 * time.Millisecond) // simulate delay
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State: true,
				Code:  0,
				Data:  json.RawMessage(fmt.Sprintf(`{"access_token":"at_refreshed_%d","refresh_token":"rt_refreshed_%d","expires_in":7200,"user_id":"u_888"}`, refreshCount, refreshCount)),
			})

		case "/open/user/info":
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State: true,
				Code:  0,
				Data:  json.RawMessage(`{"user_id":"u_888","user_name":"TestUser","is_vip":1}`),
			})

		case "/open/ufile/files":
			// Return mixed short and long fields
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State: true,
				Code:  0,
				Data: json.RawMessage(`{
					"count": 2,
					"data": [
						{
							"fid": "1001",
							"fn": "movie.mp4",
							"pc": "pick1001",
							"fs": 104857600,
							"cid": "2001",
							"fc": "1",
							"sha1": "SHA1_MOVIE_1001"
						},
						{
							"file_id": "2002",
							"file_name": "subfolder",
							"pick_code": "pick2002",
							"file_size": 0,
							"parent_id": "2001",
							"file_category": "0"
						}
					]
				}`),
			})

		case "/open/ufile/downurl":
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State: true,
				Code:  0,
				Data: json.RawMessage(`{
					"pick1001": {
						"url": "https://cdn.115.com/download/movie.mp4?sign=valid",
						"file_size": 104857600,
						"file_name": "movie.mp4",
						"file_id": "1001",
						"pick_code": "pick1001"
					}
				}`),
			})

		case "/open/offline/add_task_urls":
			_ = r.ParseForm()
			urls := r.FormValue("urls")
			hash := "ED2KHASH456"
			if idx := strings.Index(urls, "btih:"); idx >= 0 {
				hash = urls[idx+5:]
			}
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State: true,
				Code:  0,
				Data:  json.RawMessage(fmt.Sprintf(`[{"state":true,"code":0,"message":"","info_hash":"%s","url":"%s"}]`, hash, urls)),
			})

		case "/open/offline/get_task_list":
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State: true,
				Code:  0,
				Data: json.RawMessage(`{
					"count": 1,
					"tasks": [
						{
							"info_hash": "BTHASH123",
							"name": "movie.mp4",
							"size": 104857600,
							"percentDone": 100.0,
							"status": 2,
							"file_id": "1001"
						}
					]
				}`),
			})

		case "/open/ufile/delete":
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State: true,
				Code:  0,
			})

		case "/test/error/401":
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid token"})

		case "/test/error/429":
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "rate limit"})

		case "/test/error/not_found":
			_ = json.NewEncoder(w).Encode(BaseResponse{
				State:   false,
				Code:    20002,
				Message: "文件不存在",
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	server := httptest.NewServer(handler)
	return server, &refreshCount
}

func Test115ClientAuthAndSingleFlight(t *testing.T) {
	server, refreshCount := setupMock115Server()
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	auth := NewAuthClient(client, "client_id_test")

	ctx := context.Background()

	// 1. Device Auth
	deviceAuth, err := auth.StartDeviceAuth(ctx)
	if err != nil {
		t.Fatalf("StartDeviceAuth failed: %v", err)
	}
	if deviceAuth.DeviceCode != "dev_123" {
		t.Errorf("expected device_code dev_123, got %s", deviceAuth.DeviceCode)
	}
	// The mock returns a non-image qrcode URL, so the client must render a QR data URI.
	if !strings.HasPrefix(deviceAuth.QRCodeDataURI, "data:image/png;base64,") {
		t.Errorf("expected generated QR data URI, got %s", deviceAuth.QRCodeDataURI)
	}

	// 2. Poll Token
	tokens, err := auth.PollDeviceToken(ctx, deviceAuth.DeviceCode, deviceAuth.CodeVerifier)
	if err != nil {
		t.Fatalf("PollDeviceToken failed: %v", err)
	}
	if tokens.AccessToken != "at_initial" {
		t.Errorf("expected access_token at_initial, got %s", tokens.AccessToken)
	}

	// 3. Concurrent single-flight refresh test: launch 5 concurrent calls to RefreshToken
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			refreshed, err := auth.RefreshToken(ctx)
			if err != nil {
				t.Errorf("RefreshToken failed: %v", err)
			}
			if refreshed == nil || refreshed.AccessToken == "" {
				t.Errorf("expected non-empty refreshed token")
			}
		}()
	}
	wg.Wait()

	// Exactly 1 actual refresh request should have hit the mock server!
	if *refreshCount != 1 {
		t.Errorf("expected exactly 1 server refresh call due to single-flight, got %d", *refreshCount)
	}

	// 4. User info
	userInfo, err := auth.GetUserInfo(ctx)
	if err != nil {
		t.Fatalf("GetUserInfo failed: %v", err)
	}
	if userInfo.UserName != "TestUser" || !userInfo.IsVIP {
		t.Errorf("unexpected userInfo: %+v", userInfo)
	}
}

func Test115FilesAndOffline(t *testing.T) {
	server, _ := setupMock115Server()
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	client.SetAccessToken("valid_test_token")

	ctx := context.Background()

	// 1. List files & normalization
	items, count, err := client.ListFiles(ctx, "2001", 100, 0, true, "user_utime", 0)
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}
	if count != 2 || len(items) != 2 {
		t.Fatalf("expected 2 items, got %d (count=%d)", len(items), count)
	}

	fileItem := items[0]
	if fileItem.FileID != "1001" || fileItem.FileName != "movie.mp4" || fileItem.IsDir || fileItem.SizeBytes != 104857600 {
		t.Errorf("unexpected normalized fileItem: %+v", fileItem)
	}

	dirItem := items[1]
	if dirItem.FileID != "2002" || dirItem.FileName != "subfolder" || !dirItem.IsDir {
		t.Errorf("unexpected normalized dirItem: %+v", dirItem)
	}

	// 2. Download URL
	down, err := client.GetDownloadURL(ctx, "pick1001")
	if err != nil {
		t.Fatalf("GetDownloadURL failed: %v", err)
	}
	if down.URL != "https://cdn.115.com/download/movie.mp4?sign=valid" {
		t.Errorf("unexpected download url: %s", down.URL)
	}

	// 3. Add BT task
	btHash, err := client.AddBTTask(ctx, "BTHASH123", "2001")
	if err != nil {
		t.Fatalf("AddBTTask failed: %v", err)
	}
	if btHash != "BTHASH123" {
		t.Errorf("expected BTHASH123, got %s", btHash)
	}

	// 4. Add URL task
	ed2kHash, err := client.AddURLTask(ctx, "ed2k://|file|...", "2001")
	if err != nil {
		t.Fatalf("AddURLTask failed: %v", err)
	}
	if ed2kHash != "ED2KHASH456" {
		t.Errorf("expected ED2KHASH456, got %s", ed2kHash)
	}

	// 5. Delete files
	if err := client.DeleteFiles(ctx, []string{"1001"}); err != nil {
		t.Fatalf("DeleteFiles failed: %v", err)
	}
}

func Test115ErrorClassification(t *testing.T) {
	server, _ := setupMock115Server()
	defer server.Close()

	client, err := NewClient(ClientConfig{
		BaseURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx := context.Background()

	// 1. 401 Auth error
	_, err = client.DoRequest(ctx, "GET", "/test/error/401", nil, nil, "")
	if err == nil {
		t.Fatalf("expected 401 error, got nil")
	}
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Category != ErrCatAuth {
		t.Errorf("expected ErrCatAuth, got %v", err)
	}

	// 2. 429 Rate limited error with Retry-After
	_, err = client.DoRequest(ctx, "GET", "/test/error/429", nil, nil, "")
	if err == nil {
		t.Fatalf("expected 429 error, got nil")
	}
	apiErr, ok = err.(*APIError)
	if !ok || apiErr.Category != ErrCatRateLimited {
		t.Errorf("expected ErrCatRateLimited, got %v", err)
	}
	if apiErr.RetryAfter != 5*time.Second {
		t.Errorf("expected RetryAfter 5s, got %v", apiErr.RetryAfter)
	}

	// 3. Business code not found
	_, err = client.DoRequest(ctx, "GET", "/test/error/not_found", nil, nil, "")
	if err == nil {
		t.Fatalf("expected not_found error, got nil")
	}
	apiErr, ok = err.(*APIError)
	if !ok || apiErr.Category != ErrCatNotFound {
		t.Errorf("expected ErrCatNotFound, got %v", err)
	}
}
