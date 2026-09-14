package client115

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DeviceAuthResponse contains QR code, user code, and device code details.
type DeviceAuthResponse struct {
	DeviceCode   string `json:"device_code"`
	UserCode     string `json:"user_code"`
	QRCodeURL    string `json:"qrcode_url"`
	ExpiresIn    int    `json:"expires_in"`
	CodeVerifier string `json:"code_verifier,omitempty"`
}

// TokenData contains OAuth tokens and expiration info.
type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	UserID       string `json:"user_id"`
	ObtainedAt   string `json:"obtained_at"`
}

// UserInfo represents 115 user account profile.
type UserInfo struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	IsVIP    bool   `json:"is_vip"`
}

// AuthClient manages 115 authentication and token refreshing with single-flight.
type AuthClient struct {
	client        *Client
	clientID      string
	clientSecret  string
	tokenMu       sync.RWMutex
	currentTokens *TokenData

	refreshMu      sync.Mutex
	refreshCond    *sync.Cond
	isRefreshing   bool
	lastRefreshErr error
}

func NewAuthClient(client *Client, clientID, clientSecret string) *AuthClient {
	ac := &AuthClient{
		client:       client,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
	ac.refreshCond = sync.NewCond(&ac.refreshMu)
	return ac
}

// SetTokens sets active tokens in memory and updates underlying client.
func (a *AuthClient) SetTokens(t *TokenData) {
	a.tokenMu.Lock()
	defer a.tokenMu.Unlock()
	a.currentTokens = t
	if t != nil {
		a.client.SetAccessToken(t.AccessToken)
	} else {
		a.client.SetAccessToken("")
	}
}

// GetTokens returns current tokens.
func (a *AuthClient) GetTokens() *TokenData {
	a.tokenMu.RLock()
	defer a.tokenMu.RUnlock()
	return a.currentTokens
}

// GeneratePKCE creates a code_verifier and code_challenge (S256).
func GeneratePKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate random pkce verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)

	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// StartDeviceAuth requests a new device code and QR code with PKCE.
func (a *AuthClient) StartDeviceAuth(ctx context.Context) (*DeviceAuthResponse, error) {
	endpoint := "/open/authDeviceCode"
	form := url.Values{}
	if a.clientID != "" {
		form.Set("client_id", a.clientID)
	}

	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "S256")

	resp, err := a.client.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return nil, fmt.Errorf("start device auth: %w", err)
	}

	var authResp DeviceAuthResponse
	if err := json.Unmarshal(resp.Data, &authResp); err != nil {
		return nil, fmt.Errorf("unmarshal device auth response: %w", err)
	}
	authResp.CodeVerifier = verifier

	return &authResp, nil
}

// PollDeviceToken polls if the user has confirmed QR code scan.
func (a *AuthClient) PollDeviceToken(ctx context.Context, deviceCode, codeVerifier string) (*TokenData, error) {
	endpoint := "/open/deviceCodeToToken"
	form := url.Values{}
	form.Set("device_code", deviceCode)
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}
	if a.clientID != "" {
		form.Set("client_id", a.clientID)
	}

	resp, err := a.client.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return nil, err
	}

	var tokens TokenData
	if err := json.Unmarshal(resp.Data, &tokens); err != nil {
		return nil, fmt.Errorf("unmarshal token response: %w", err)
	}
	tokens.ObtainedAt = time.Now().UTC().Format(time.RFC3339)

	a.SetTokens(&tokens)
	return &tokens, nil
}

// RefreshToken executes a single-flight token refresh.
func (a *AuthClient) RefreshToken(ctx context.Context) (*TokenData, error) {
	a.refreshMu.Lock()

	// If another goroutine is already refreshing, wait for it
	if a.isRefreshing {
		for a.isRefreshing {
			a.refreshCond.Wait()
		}
		a.refreshMu.Unlock()
		if a.lastRefreshErr != nil {
			return nil, a.lastRefreshErr
		}
		return a.GetTokens(), nil
	}

	a.isRefreshing = true
	a.lastRefreshErr = nil
	a.refreshMu.Unlock()

	defer func() {
		a.refreshMu.Lock()
		a.isRefreshing = false
		a.refreshCond.Broadcast()
		a.refreshMu.Unlock()
	}()

	current := a.GetTokens()
	if current == nil || current.RefreshToken == "" {
		err := fmt.Errorf("no refresh token available")
		a.lastRefreshErr = err
		return nil, err
	}

	endpoint := "/open/refreshToken"
	form := url.Values{}
	form.Set("refresh_token", current.RefreshToken)
	if a.clientID != "" {
		form.Set("client_id", a.clientID)
	}

	resp, err := a.client.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		a.lastRefreshErr = fmt.Errorf("execute refresh token request: %w", err)
		return nil, a.lastRefreshErr
	}

	var newTokens TokenData
	if err := json.Unmarshal(resp.Data, &newTokens); err != nil {
		a.lastRefreshErr = fmt.Errorf("unmarshal refreshed tokens: %w", err)
		return nil, a.lastRefreshErr
	}
	newTokens.ObtainedAt = time.Now().UTC().Format(time.RFC3339)

	a.SetTokens(&newTokens)
	return &newTokens, nil
}

// GetUserInfo fetches basic user profile information.
func (a *AuthClient) GetUserInfo(ctx context.Context) (*UserInfo, error) {
	endpoint := "/open/user/info"
	resp, err := a.client.DoRequest(ctx, "GET", endpoint, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("get user info: %w", err)
	}

	var raw struct {
		UserID   string `json:"user_id"`
		UserName string `json:"user_name"`
		IsVIP    int    `json:"is_vip"`
	}
	if err := json.Unmarshal(resp.Data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal user info: %w", err)
	}

	return &UserInfo{
		UserID:   raw.UserID,
		UserName: raw.UserName,
		IsVIP:    raw.IsVIP == 1,
	}, nil
}
