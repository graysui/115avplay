package client115

import (
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
	BaseURL     string        // Default: https://proapi.115.com or https://passportapi.115.com
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
}

// NewClient creates a new 115 OpenAPI client.
func NewClient(cfg ClientConfig) (*Client, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://proapi.115.com"
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.UserAgent == "" {
		cfg.UserAgent = "MediaVault/1.0"
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 4
	}

	transport := &http.Transport{
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
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
	}, nil
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
func (c *Client) DoRequest(ctx context.Context, method, endpoint string, query url.Values, body io.Reader, contentType string) (*BaseResponse, error) {
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

	req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
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
			Message:    fmt.Sprintf("invalid JSON response: %v", err),
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
	if strings.Contains(msgLower, "access limit") || strings.Contains(msgLower, "访问上限") || strings.Contains(msgLower, "频繁") || code == 990002 {
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
