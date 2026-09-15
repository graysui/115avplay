package client115

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrorCategory categorizes 115 API errors according to design §8.4.
type ErrorCategory string

const (
	ErrCatNotFound    ErrorCategory = "not_found"
	ErrCatAuth        ErrorCategory = "auth"
	ErrCatRateLimited ErrorCategory = "rate_limited"
	ErrCatQuota       ErrorCategory = "quota"
	ErrCatTransient   ErrorCategory = "transient"
	ErrCatUnknown     ErrorCategory = "unknown"
)

// APIError represents an error returned by the 115 API or HTTP client.
type APIError struct {
	Category   ErrorCategory
	StatusCode int
	ErrorCode  int
	Message    string
	RetryAfter time.Duration
	RawBody    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("115 API error [%s] HTTP %d (code=%d): %s", e.Category, e.StatusCode, e.ErrorCode, e.Message)
}

// ClientConfig holds configuration for the 115 OpenAPI client.
type ClientConfig struct {
	BaseURL     string        // Default: https://proapi.115.com (file/offline API)
	AuthBaseURL string        // Default: https://passportapi.115.com (OAuth endpoints)
	WebBaseURL  string        // Default: https://webapi.115.com (cookie web API)
	Cookie      string        // Optional 115 web cookie (UID/CID/SEID/KID) for listing
	ProxyURL    string        // Optional HTTP/SOCKS5 proxy
	Timeout     time.Duration // Default: 15s
	UserAgent   string        // Default: MediaVault/1.0
	Concurrency int           // Concurrency slot limit
}

// Client is the core HTTP client for 115 OpenAPI.
type Client struct {
	httpClient  *http.Client
	config      ClientConfig
	semaphore   chan struct{}
	authMu      sync.RWMutex
	accessToken string
	cookieMu    sync.RWMutex
	cookie      string
	refreshMu   sync.Mutex
	refreshFn   func(context.Context) error
	refreshing  bool

	dlURLMu    sync.Mutex
	dlURLCache map[string]downloadURLCacheEntry
}

type downloadURLCacheEntry struct {
	res       *DownloadURLResponse
	expiresAt time.Time
}

// cacheDownloadURL stores a freshly obtained direct link. It expires 60 seconds
// before the link's own `t` timestamp (or after 5 minutes if unknown), so rapid
// seek/range requests reuse the same link instead of re-resolving it.
func (c *Client) cacheDownloadURL(pickCode string, res *DownloadURLResponse) {
	if res == nil || res.URL == "" {
		return
	}
	exp := time.Now().Add(5 * time.Minute)
	if u, err := url.Parse(res.URL); err == nil {
		if t := u.Query().Get("t"); t != "" {
			if ts, err := strconv.ParseInt(t, 10, 64); err == nil {
				exp = time.Unix(ts, 0).Add(-60 * time.Second)
			}
		}
	}
	if exp.Before(time.Now().Add(30 * time.Second)) {
		exp = time.Now().Add(30 * time.Second)
	}
	c.dlURLMu.Lock()
	if c.dlURLCache == nil {
		c.dlURLCache = map[string]downloadURLCacheEntry{}
	}
	c.dlURLCache[pickCode] = downloadURLCacheEntry{res: res, expiresAt: exp}
	c.dlURLMu.Unlock()
}

func (c *Client) cachedDownloadURL(pickCode string) (*DownloadURLResponse, bool) {
	c.dlURLMu.Lock()
	defer c.dlURLMu.Unlock()
	e, ok := c.dlURLCache[pickCode]
	if !ok || time.Now().After(e.expiresAt) {
		return nil, false
	}
	return e.res, true
}

// SetTokenRefresher registers a callback used to refresh the OAuth access token
// when an API call fails with an authentication error.
func (c *Client) SetTokenRefresher(fn func(context.Context) error) {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.refreshFn = fn
}

// NewClient creates a new 115 OpenAPI client.
func NewClient(cfg ClientConfig) (*Client, error) {
	customBase := cfg.BaseURL != ""
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://proapi.115.com"
	}
	if cfg.AuthBaseURL == "" {
		// OAuth endpoints live on passportapi.115.com; when a custom BaseURL is
		// supplied (tests / self-hosted proxy) reuse it so all traffic is mocked.
		if customBase {
			cfg.AuthBaseURL = cfg.BaseURL
		} else {
			cfg.AuthBaseURL = "https://passportapi.115.com"
		}
	}
	if cfg.WebBaseURL == "" {
		if customBase {
			cfg.WebBaseURL = cfg.BaseURL
		} else {
			cfg.WebBaseURL = "https://webapi.115.com"
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.UserAgent == "" {
		// A 115 app User-Agent makes the OpenAPI return CDN links without the
		// "f=1" User-Agent lock, so Emby clients can direct-play them.
		cfg.UserAgent = "115disk/2.0"
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}

	transport := &http.Transport{
		MaxIdleConns:       10,
		IdleConnTimeout:    90 * time.Second,
		DisableCompression: false,
	}

	if cfg.ProxyURL != "" {
		proxy, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid 115 proxy url: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxy)
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   cfg.Timeout,
	}

	return &Client{
		httpClient: httpClient,
		config:     cfg,
		semaphore:  make(chan struct{}, cfg.Concurrency),
		cookie:     cfg.Cookie,
	}, nil
}

// SetCookie sets the 115 web cookie used for cookie-based listing.
func (c *Client) SetCookie(cookie string) {
	c.cookieMu.Lock()
	defer c.cookieMu.Unlock()
	c.cookie = cookie
}

// GetCookie returns the configured 115 web cookie.
func (c *Client) GetCookie() string {
	c.cookieMu.RLock()
	defer c.cookieMu.RUnlock()
	return c.cookie
}

// WebBaseURL returns the base URL for the cookie web API.
func (c *Client) WebBaseURL() string {
	return c.config.WebBaseURL
}

// SetAccessToken sets the current active OAuth access token.
func (c *Client) SetAccessToken(token string) {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	c.accessToken = token
}

// GetAccessToken gets the current active OAuth access token.
func (c *Client) GetAccessToken() string {
	c.authMu.RLock()
	defer c.authMu.RUnlock()
	return c.accessToken
}

// GetAuthBaseURL returns the base URL used for OAuth endpoints.
func (c *Client) GetAuthBaseURL() string {
	return c.config.AuthBaseURL
}

// BaseResponse represents the common 115 JSON response structure.
type BaseResponse struct {
	State   interface{}     `json:"state"`
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Error   string          `json:"error"`
	ErrNo   int             `json:"errno"`
	Data    json.RawMessage `json:"data"`
}

// IsSuccess returns true if state is true/1/non-zero and code is 0.
func (b *BaseResponse) IsSuccess() bool {
	if b.Code != 0 {
		return false
	}
	switch v := b.State.(type) {
	case bool:
		return v
	case float64:
		return v != 0
	case int:
		return v != 0
	case string:
		return v == "1" || strings.ToLower(v) == "true"
	}
	return true
}

// DoRequest performs an authenticated HTTP request with concurrency control and error classification.
// DoRequest performs an OpenAPI request. If the server reports an authentication
// error, it refreshes the OAuth access token once (if a refresher is registered)
// and retries, so every 115 call benefits from automatic token renewal.
func (c *Client) DoRequest(ctx context.Context, method, endpoint string, query url.Values, body io.Reader, contentType string) (*BaseResponse, error) {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = io.ReadAll(body)
	}

	resp, err := c.doRequestOnce(ctx, method, endpoint, query, bodyBytes, contentType)
	if err == nil {
		return resp, nil
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Category != ErrCatAuth {
		return resp, err
	}

	// Refresh once, but never recurse (the refresh request itself goes through
	// DoRequest too).
	c.refreshMu.Lock()
	fn := c.refreshFn
	if fn == nil || c.refreshing {
		c.refreshMu.Unlock()
		return resp, err
	}
	c.refreshing = true
	c.refreshMu.Unlock()

	rerr := fn(ctx)

	c.refreshMu.Lock()
	c.refreshing = false
	c.refreshMu.Unlock()
	if rerr != nil {
		return resp, err
	}

	return c.doRequestOnce(ctx, method, endpoint, query, bodyBytes, contentType)
}

func (c *Client) doRequestOnce(ctx context.Context, method, endpoint string, query url.Values, body []byte, contentType string) (*BaseResponse, error) {
	// Acquire concurrency slot
	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	fullURL := endpoint
	if !strings.HasPrefix(fullURL, "http://") && !strings.HasPrefix(fullURL, "https://") {
		fullURL = strings.TrimRight(c.config.BaseURL, "/") + "/" + strings.TrimLeft(endpoint, "/")
	}

	if len(query) > 0 {
		if strings.Contains(fullURL, "?") {
			fullURL += "&" + query.Encode()
		} else {
			fullURL += "?" + query.Encode()
		}
	}

	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("User-Agent", c.config.UserAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Add Bearer token if present
	token := c.GetAccessToken()
	if token != "" && req.Header.Get("Authorization") == "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &APIError{
			Category: ErrCatTransient,
			Message:  err.Error(),
		}
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &APIError{
			Category:   ErrCatTransient,
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("read response body: %v", err),
		}
	}

	// Check HTTP status code
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &APIError{
			Category:   ErrCatAuth,
			StatusCode: resp.StatusCode,
			Message:    "unauthorized (token expired or invalid)",
			RawBody:    string(respBytes),
		}
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, &APIError{
			Category:   ErrCatRateLimited,
			StatusCode: resp.StatusCode,
			Message:    "rate limited (429)",
			RetryAfter: retryAfter,
			RawBody:    string(respBytes),
		}
	}

	if resp.StatusCode >= 500 {
		return nil, &APIError{
			Category:   ErrCatTransient,
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("server error: %s", resp.Status),
			RawBody:    string(respBytes),
		}
	}

	var base BaseResponse
	if err := json.Unmarshal(respBytes, &base); err != nil {
		return nil, &APIError{
			Category:   ErrCatTransient,
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("invalid JSON response (HTTP %d): %v; body=%.300s", resp.StatusCode, err, string(respBytes)),
			RawBody:    string(respBytes),
		}
	}

	// Classify 115 business error codes
	if !base.IsSuccess() {
		cat := classifyErrorCode(base.Code, base.Message)
		return nil, &APIError{
			Category:   cat,
			StatusCode: resp.StatusCode,
			ErrorCode:  base.Code,
			Message:    base.Message,
			RawBody:    string(respBytes),
		}
	}

	return &base, nil
}

func classifyErrorCode(code int, msg string) ErrorCategory {
	msgLower := strings.ToLower(msg)
	if strings.Contains(msgLower, "token") || strings.Contains(msgLower, "auth") || code == 990001 || code == 401 {
		return ErrCatAuth
	}
	// Only treat 990002 as rate limiting when the message actually says so; 115 also
	// uses 990002 for plain parameter errors ("参数错误").
	if strings.Contains(msgLower, "access limit") || strings.Contains(msgLower, "访问上限") ||
		strings.Contains(msgLower, "频繁") || strings.Contains(msgLower, "rate limit") ||
		strings.Contains(msgLower, "too many") {
		return ErrCatRateLimited
	}
	if strings.Contains(msgLower, "不存在") || strings.Contains(msgLower, "not exist") || strings.Contains(msgLower, "not found") || code == 20002 || code == 404 {
		return ErrCatNotFound
	}
	if strings.Contains(msgLower, "空间不足") || strings.Contains(msgLower, "配额") || strings.Contains(msgLower, "quota") {
		return ErrCatQuota
	}
	return ErrCatUnknown
}

func parseRetryAfter(val string) time.Duration {
	if val == "" {
		return 3 * time.Second
	}
	if sec, err := strconv.Atoi(val); err == nil && sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return 3 * time.Second
}
