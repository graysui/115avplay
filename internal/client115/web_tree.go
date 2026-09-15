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

// FlatFolder is a folder node from the bulk "download folders" API.
type FlatFolder struct {
	FID  string
	Name string
	PID  string
}

// FlatFile is a file node from the bulk "download files" API (no name).
type FlatFile struct {
	PickCode  string
	PID       string
	SizeBytes int64
	SHA1      string
}

// webGetJSON performs a cookie-authenticated GET against the 115 web API.
func (c *Client) webGetJSON(ctx context.Context, path string, params url.Values) ([]byte, error) {
	if c.GetCookie() == "" {
		return nil, &APIError{Category: ErrCatAuth, Message: "未配置 115 网页 Cookie"}
	}
	endpoint := c.WebBaseURL() + path
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}

	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", c.GetCookie())
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36")
	req.Header.Set("Referer", "https://115.com/")
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &APIError{Category: ErrCatTransient, Message: err.Error()}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &APIError{Category: ErrCatAuth, StatusCode: resp.StatusCode, Message: "115 Cookie 已失效或无权限"}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &APIError{Category: ErrCatRateLimited, StatusCode: resp.StatusCode, Message: "115 请求过于频繁", RetryAfter: 3 * time.Second}
	case resp.StatusCode != http.StatusOK:
		return nil, &APIError{Category: ErrCatTransient, StatusCode: resp.StatusCode, Message: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncateStr(string(body), 200))}
	}
	return body, nil
}

type webEnvelope struct {
	State       interface{}     `json:"state"`
	ErrNo       int             `json:"errno"`
	ErrMsg      string          `json:"error"`
	HasNextPage bool            `json:"has_next_page"`
	Data        json.RawMessage `json:"data"`
	Count       int             `json:"count"`
}

func (e *webEnvelope) check() error {
	if stateIsTrue(e.State) {
		return nil
	}
	msg := e.ErrMsg
	if msg == "" {
		msg = "115 返回错误"
	}
	return &APIError{Category: classifyErrorCode(e.ErrNo, msg), ErrorCode: e.ErrNo, Message: msg}
}

// GetNodePickCode returns the pickcode of a folder/file id.
func (c *Client) GetNodePickCode(ctx context.Context, cid string) (string, error) {
	q := url.Values{}
	q.Set("file_id", cid)
	body, err := c.webGetJSON(ctx, "/files/file", q)
	if err != nil {
		return "", err
	}
	var env webEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return "", fmt.Errorf("decode node info: %w", err)
	}
	if err := env.check(); err != nil {
		return "", err
	}

	// data may be an object or an array of nodes.
	var one struct {
		PickCode string `json:"pick_code"`
		Pc       string `json:"pc"`
	}
	if err := json.Unmarshal(env.Data, &one); err == nil && (one.PickCode != "" || one.Pc != "") {
		if one.PickCode != "" {
			return one.PickCode, nil
		}
		return one.Pc, nil
	}
	var arr []struct {
		PickCode string `json:"pick_code"`
		Pc       string `json:"pc"`
	}
	if err := json.Unmarshal(env.Data, &arr); err == nil && len(arr) > 0 {
		if arr[0].PickCode != "" {
			return arr[0].PickCode, nil
		}
		return arr[0].Pc, nil
	}
	return "", nil
}

// DownloadFolders returns every descendant folder of a directory (pickcode),
// using the bulk /files/downfolders API (5000 per page).
func (c *Client) DownloadFolders(ctx context.Context, pickcode string) ([]FlatFolder, error) {
	var out []FlatFolder
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("pickcode", pickcode)
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", "5000")

		body, err := c.webGetJSON(ctx, "/files/downfolders", q)
		if err != nil {
			return nil, err
		}
		var env webEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, fmt.Errorf("decode downfolders: %w", err)
		}
		if err := env.check(); err != nil {
			return nil, err
		}

		var data struct {
			List []struct {
				FID string      `json:"fid"`
				FN  string      `json:"fn"`
				PID interface{} `json:"pid"`
			} `json:"list"`
		}
		_ = json.Unmarshal(env.Data, &data)

		for _, it := range data.List {
			out = append(out, FlatFolder{FID: anyToString(it.FID), Name: it.FN, PID: anyToString(it.PID)})
		}
		if !env.HasNextPage || len(data.List) == 0 {
			break
		}
	}
	return out, nil
}

// DownloadFiles returns every descendant file of a directory (pickcode), using
// the bulk /files/downfiles API (5000 per page). Names are not included.
func (c *Client) DownloadFiles(ctx context.Context, pickcode string) ([]FlatFile, error) {
	var out []FlatFile
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("pickcode", pickcode)
		q.Set("page", strconv.Itoa(page))
		q.Set("per_page", "5000")

		body, err := c.webGetJSON(ctx, "/files/downfiles", q)
		if err != nil {
			return nil, err
		}
		var env webEnvelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, fmt.Errorf("decode downfiles: %w", err)
		}
		if err := env.check(); err != nil {
			return nil, err
		}

		var data struct {
			List []struct {
				PC   string      `json:"pc"`
				PID  interface{} `json:"pid"`
				FS   interface{} `json:"fs"`
				SHA1 string      `json:"sha1"`
			} `json:"list"`
		}
		_ = json.Unmarshal(env.Data, &data)

		for _, it := range data.List {
			out = append(out, FlatFile{
				PickCode:  it.PC,
				PID:       anyToString(it.PID),
				SizeBytes: anyToInt64(it.FS),
				SHA1:      it.SHA1,
			})
		}
		if !env.HasNextPage || len(data.List) == 0 {
			break
		}
	}
	return out, nil
}

// GetDownloadURLWeb obtains a CDN download link via the cookie web API
// (GET /files/download?pickcode=...). Used when the OpenAPI token is invalid.
func (c *Client) GetDownloadURLWeb(ctx context.Context, pickCode string) (*DownloadURLResponse, error) {
	q := url.Values{}
	q.Set("pickcode", pickCode)
	body, err := c.webGetJSON(ctx, "/files/download", q)
	if err != nil {
		return nil, err
	}
	var env webEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("decode download url: %w", err)
	}
	if err := env.check(); err != nil {
		return nil, err
	}

	// data.url may be a nested object {"url": "..."} or a plain string.
	var data struct {
		URL      json.RawMessage `json:"url"`
		FileName string          `json:"file_name"`
		FileSize int64           `json:"file_size"`
		PickCode string          `json:"pick_code"`
	}
	_ = json.Unmarshal(env.Data, &data)

	urlStr := ""
	if len(data.URL) > 0 {
		var s string
		if json.Unmarshal(data.URL, &s) == nil {
			urlStr = s
		} else {
			var nested struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(data.URL, &nested) == nil {
				urlStr = nested.URL
			}
		}
	}
	if urlStr == "" {
		return nil, &APIError{Category: ErrCatNotFound, Message: "115 未返回下载直链"}
	}

	return &DownloadURLResponse{
		URL:        urlStr,
		PickCode:   pickCode,
		FileName:   data.FileName,
		FileSize:   data.FileSize,
		ObtainedAt: time.Now().UTC(),
	}, nil
}

// GetDownloadURLAuto prefers the cookie web API (reliable even when the OAuth
// access token has expired) and falls back to the OpenAPI.
func (c *Client) GetDownloadURLAuto(ctx context.Context, pickCode string) (*DownloadURLResponse, error) {
	// Reuse a still-valid direct link so seeks / repeated range requests do not
	// re-resolve against 115 every time.
	if res, ok := c.cachedDownloadURL(pickCode); ok {
		return res, nil
	}

	var res *DownloadURLResponse
	var err error
	if strings.TrimSpace(c.GetCookie()) != "" {
		res, err = c.GetDownloadURLWeb(ctx, pickCode)
		if err != nil || res == nil {
			res, err = c.GetDownloadURL(ctx, pickCode)
		}
	} else {
		res, err = c.GetDownloadURL(ctx, pickCode)
	}
	if err != nil {
		return nil, err
	}
	c.cacheDownloadURL(pickCode, res)
	return res, nil
}
