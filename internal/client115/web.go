package client115

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ListFilesWeb lists one directory using the cookie-based web API
// (https://webapi.115.com/files). This is the reliable way to walk the folder
// tree: the OpenAPI /open/ufile/files endpoint is WAF-protected and returns
// HTTP 405 (an HTML error page) when listed in bulk.
func (c *Client) ListFilesWeb(ctx context.Context, cid string, limit, offset int, showDir bool) ([]FileItem, int, error) {
	if limit <= 0 {
		limit = 1000
	}
	showDirVal := "0"
	if showDir {
		showDirVal = "1"
	}

	q := url.Values{}
	q.Set("aid", "1")
	q.Set("cid", cid)
	q.Set("show_dir", showDirVal)
	q.Set("limit", strconv.Itoa(limit))
	q.Set("offset", strconv.Itoa(offset))
	q.Set("o", "user_utime")
	q.Set("asc", "0")
	q.Set("natsort", "1")
	q.Set("custom_order", "1")
	q.Set("fc_mix", "0")

	endpoint := strings.TrimRight(c.WebBaseURL(), "/") + "/files?" + q.Encode()

	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("create web request: %w", err)
	}
	req.Header.Set("Cookie", c.GetCookie())
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, &APIError{Category: ErrCatTransient, Message: err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, 0, &APIError{Category: ErrCatAuth, StatusCode: resp.StatusCode, Message: "115 Cookie 已失效或无权限", RawBody: truncateStr(string(body), 200)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, 0, &APIError{Category: ErrCatRateLimited, StatusCode: resp.StatusCode, Message: "115 请求过于频繁", RetryAfter: 3 * time.Second}
	case resp.StatusCode != http.StatusOK:
		return nil, 0, &APIError{Category: ErrCatTransient, StatusCode: resp.StatusCode, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 200))}
	}

	var raw struct {
		State  interface{}   `json:"state"`
		Count  int           `json:"count"`
		ErrNo  int           `json:"errno"`
		ErrMsg string        `json:"error_msg"`
		Data   []RawFileItem `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, 0, &APIError{Category: ErrCatTransient, Message: fmt.Sprintf("invalid JSON: %v; body=%.200s", err, string(body))}
	}

	if !stateIsTrue(raw.State) {
		msg := raw.ErrMsg
		if msg == "" {
			msg = "115 返回错误"
		}
		return nil, 0, &APIError{Category: classifyErrorCode(raw.ErrNo, msg), ErrorCode: raw.ErrNo, Message: msg}
	}

	items := make([]FileItem, len(raw.Data))
	for i, r := range raw.Data {
		items[i] = r.Normalize()
	}
	return items, raw.Count, nil
}

func stateIsTrue(v interface{}) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	case nil:
		// Some responses omit state on success.
		return true
	}
	return false
}

func truncateStr(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
