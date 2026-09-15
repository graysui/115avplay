package client115

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
)

// DeviceAuthResponse contains QR code, user code, and device code details.
// 115's open API returns a uid/time/sign triplet; the QR image is served by
// qrcodeapi.115.com. Legacy field names are kept for compatibility.
type DeviceAuthResponse struct {
	UID           string `json:"uid"`
	Time          int64  `json:"time"`
	Sign          string `json:"sign"`
	QRCode        string `json:"qrcode"`
	QRCodeURL     string `json:"qrcode_url"`
	QRCodeDataURI string `json:"qrcode_data_uri,omitempty"`
	ExpiresIn     int    `json:"expires_in"`
	CodeVerifier  string `json:"code_verifier,omitempty"`

	// Normalized aliases exposed to the admin UI.
	DeviceCode string `json:"device_code"`
	UserCode   string `json:"user_code"`
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
	authBaseURL   string
	clientID      string
	tokenMu       sync.RWMutex
	currentTokens *TokenData

	refreshMu      sync.Mutex
	refreshCond    *sync.Cond
	isRefreshing   bool
	lastRefreshErr error
}

func NewAuthClient(client *Client, clientID string) *AuthClient {
	ac := &AuthClient{
		client:      client,
		authBaseURL: client.GetAuthBaseURL(),
		clientID:    clientID,
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

// ClientID returns the configured 115 OpenAPI application ID (empty if unset).
func (a *AuthClient) ClientID() string {
	return a.clientID
}

// SetClientID updates the OpenAPI application ID at runtime so changes made in the
// admin UI take effect without restarting the process.
func (a *AuthClient) SetClientID(clientID string) {
	a.clientID = clientID
}

// GeneratePKCE creates a code_verifier and code_challenge.
// 115's open platform expects code_challenge_method=sha256 (not the RFC "S256" label)
// and a standard Base64 encoded SHA-256 digest, matching the reference client.
func GeneratePKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate random pkce verifier: %w", err)
	}
	verifier = base64.StdEncoding.EncodeToString(raw)

	h := sha256.Sum256([]byte(verifier))
	challenge = base64.StdEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// StartDeviceAuth requests a new device code and QR code with PKCE.
func (a *AuthClient) StartDeviceAuth(ctx context.Context) (*DeviceAuthResponse, error) {
	endpoint := a.authBaseURL + "/open/authDeviceCode"
	form := url.Values{}
	if a.clientID != "" {
		form.Set("client_id", a.clientID)
	}

	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	form.Set("code_challenge", challenge)
	form.Set("code_challenge_method", "sha256")

	resp, err := a.client.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return nil, fmt.Errorf("start device auth: %w", err)
	}

	var authResp DeviceAuthResponse
	if err := json.Unmarshal(resp.Data, &authResp); err != nil {
		return nil, fmt.Errorf("unmarshal device auth response: %w", err)
	}
	authResp.CodeVerifier = verifier

	// Normalize: 115 may return a uid/time/sign triplet, an image URL (qrcodeapi.115.com)
	// or a scan landing URL (https://115.com/scan/...) that must be rendered as a QR.
	if authResp.DeviceCode == "" {
		authResp.DeviceCode = authResp.UID
	}
	if authResp.DeviceCode == "" {
		authResp.DeviceCode = scanToken(authResp.QRCode, authResp.QRCodeURL)
	}

	qrRef := authResp.QRCodeURL
	if qrRef == "" {
		qrRef = authResp.QRCode
	}
	if qrRef == "" && authResp.UID != "" {
		qrRef = "https://qrcodeapi.115.com/api/1.0/web/2.0/qrcode?uid=" + url.QueryEscape(authResp.UID)
	}
	authResp.QRCodeURL = qrRef
	if authResp.ExpiresIn <= 0 {
		authResp.ExpiresIn = 300
	}

	// Prefer fetching a real QR image; otherwise the value is the content the user
	// scans, so render a QR code for it ourselves.
	if qrRef != "" {
		if dataURI, ferr := fetchQRCodeDataURI(ctx, qrRef); ferr == nil {
			authResp.QRCodeDataURI = dataURI
		} else if png, qerr := qrcode.Encode(qrRef, qrcode.Medium, 320); qerr == nil {
			authResp.QRCodeDataURI = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
		}
	}

	return &authResp, nil
}

// scanToken extracts a device-grant token from a 115 scan URL (…/scan/dg-xxxx).
func scanToken(refs ...string) string {
	for _, r := range refs {
		if r == "" {
			continue
		}
		u, err := url.Parse(r)
		if err != nil {
			continue
		}
		seg := path.Base(u.Path)
		if seg != "" && seg != "/" && seg != "." {
			return seg
		}
	}
	return ""
}

// fetchQRCodeDataURI downloads a QR image and returns it as a data URI.
func fetchQRCodeDataURI(ctx context.Context, qrURL string) (string, error) {
	if !strings.HasPrefix(qrURL, "http://") && !strings.HasPrefix(qrURL, "https://") {
		return "", fmt.Errorf("unsupported qr url: %s", qrURL)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", qrURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "MediaVault/1.0")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("qr fetch status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || len(data) == 0 {
		return "", fmt.Errorf("read qr image: %v", err)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/") {
		ct = http.DetectContentType(data)
	}
	// The response must actually be an image; otherwise the caller renders a QR.
	if !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("response is not an image (content-type %s)", ct)
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// PollDeviceToken polls if the user has confirmed QR code scan.
func (a *AuthClient) PollDeviceToken(ctx context.Context, deviceCode, codeVerifier string) (*TokenData, error) {
	endpoint := a.authBaseURL + "/open/deviceCodeToToken"
	form := url.Values{}
	form.Set("device_code", deviceCode)
	// 115's open API expects the uid returned by authDeviceCode; send it both ways.
	form.Set("uid", deviceCode)
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

	endpoint := a.authBaseURL + "/open/refreshToken"
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
