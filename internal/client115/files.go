package client115

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FileItem represents a normalized file or directory entry from 115 OpenAPI.
type FileItem struct {
	FileID    string `json:"file_id"`
	FileName  string `json:"file_name"`
	PickCode  string `json:"pick_code"`
	SizeBytes int64  `json:"size_bytes"`
	ParentID  string `json:"parent_id"`
	IsDir     bool   `json:"is_dir"`
	SHA1      string `json:"sha1,omitempty"`
	Utime     int64  `json:"utime,omitempty"`
}

// DownloadURLResponse contains the CDN download link and expiration.
type DownloadURLResponse struct {
	URL        string    `json:"url"`
	PickCode   string    `json:"pick_code"`
	FileID     string    `json:"file_id"`
	FileName   string    `json:"file_name"`
	FileSize   int64     `json:"file_size"`
	ObtainedAt time.Time `json:"obtained_at"`
}

// RawFileItem handles heterogeneous long and short field names from 115 API.
type RawFileItem struct {
	FileID       interface{} `json:"file_id"`
	Fid          interface{} `json:"fid"`
	Cid          interface{} `json:"cid"`
	FileName     string      `json:"file_name"`
	Fn           string      `json:"fn"`
	N            string      `json:"n"`
	PickCode     string      `json:"pick_code"`
	Pc           string      `json:"pc"`
	FileSize     interface{} `json:"file_size"`
	Fs           interface{} `json:"fs"`
	S            interface{} `json:"s"`
	FileCategory interface{} `json:"file_category"`
	Fc           interface{} `json:"fc"`
	IsDir        interface{} `json:"is_dir"`
	ParentID     interface{} `json:"parent_id"`
	Pid          interface{} `json:"pid"`
	SHA1         string      `json:"sha1"`
	Sha          string      `json:"sha"`
	Uptime       interface{} `json:"user_utime"`
	Upt          interface{} `json:"upt"`
	Te           interface{} `json:"te"`
}

func (r *RawFileItem) Normalize() FileItem {
	item := FileItem{}

	// 1. File ID
	item.FileID = anyToString(r.FileID)
	if item.FileID == "" {
		item.FileID = anyToString(r.Fid)
	}
	if item.FileID == "" && r.IsDirBool() {
		item.FileID = anyToString(r.Cid)
	}

	// 2. File Name
	item.FileName = r.FileName
	if item.FileName == "" {
		item.FileName = r.Fn
	}
	if item.FileName == "" {
		item.FileName = r.N
	}

	// 3. Pick Code
	item.PickCode = r.PickCode
	if item.PickCode == "" {
		item.PickCode = r.Pc
	}

	// 4. Size Bytes
	item.SizeBytes = anyToInt64(r.FileSize)
	if item.SizeBytes == 0 {
		item.SizeBytes = anyToInt64(r.Fs)
	}
	if item.SizeBytes == 0 {
		item.SizeBytes = anyToInt64(r.S)
	}

	// 5. Parent ID
	item.ParentID = anyToString(r.ParentID)
	if item.ParentID == "" {
		item.ParentID = anyToString(r.Pid)
	}
	if item.ParentID == "" && !r.IsDirBool() {
		item.ParentID = anyToString(r.Cid)
	}

	// 6. IsDir
	item.IsDir = r.IsDirBool()

	// 7. SHA1
	item.SHA1 = r.SHA1
	if item.SHA1 == "" {
		item.SHA1 = r.Sha
	}

	// 8. Utime
	item.Utime = anyToInt64(r.Uptime)
	if item.Utime == 0 {
		item.Utime = anyToInt64(r.Upt)
	}
	if item.Utime == 0 {
		item.Utime = anyToInt64(r.Te)
	}

	return item
}

func (r *RawFileItem) IsDirBool() bool {
	fc := anyToString(r.FileCategory)
	if fc == "0" {
		return true
	}
	if fc == "1" {
		return false
	}
	fcShort := anyToString(r.Fc)
	if fcShort == "0" {
		return true
	}
	if fcShort == "1" {
		return false
	}
	isDirStr := anyToString(r.IsDir)
	if isDirStr == "1" || isDirStr == "true" {
		return true
	}
	return false
}

// ListFiles lists direct files and/or folders in a directory.
func (c *Client) ListFiles(ctx context.Context, cid string, limit, offset int, showDir bool, order string, asc int) ([]FileItem, int, error) {
	if limit <= 0 {
		limit = 100
	}
	if order == "" {
		order = "user_utime"
	}

	showDirVal := "0"
	if showDir {
		showDirVal = "1"
	}

	query := url.Values{}
	query.Set("cid", cid)
	query.Set("limit", strconv.Itoa(limit))
	query.Set("offset", strconv.Itoa(offset))
	query.Set("show_dir", showDirVal)
	query.Set("o", order)
	query.Set("asc", strconv.Itoa(asc))
	query.Set("custom_order", "1")
	query.Set("fc_mix", "0")
	query.Set("natsort", "1")

	endpoint := "/open/ufile/files"
	resp, err := c.DoRequest(ctx, "GET", endpoint, query, nil, "")
	if err != nil {
		return nil, 0, fmt.Errorf("list files: %w", err)
	}

	var rawData struct {
		Count int           `json:"count"`
		Data  []RawFileItem `json:"data"`
	}
	if err := json.Unmarshal(resp.Data, &rawData); err != nil {
		// Sometimes data is directly an array
		var arr []RawFileItem
		if err2 := json.Unmarshal(resp.Data, &arr); err2 == nil {
			items := make([]FileItem, len(arr))
			for i, r := range arr {
				items[i] = r.Normalize()
			}
			return items, len(items), nil
		}
		return nil, 0, fmt.Errorf("unmarshal list files data: %w", err)
	}

	items := make([]FileItem, len(rawData.Data))
	for i, r := range rawData.Data {
		items[i] = r.Normalize()
	}

	return items, rawData.Count, nil
}

// WalkTree traverses a folder tree using the OpenAPI (one directory per request)
// and invokes onFile for every file. The 115 OpenAPI has no recursive listing, so
// directories are listed level by level with bounded concurrency. onProgress is
// called every 50 directories with the running directory/file counts.
func (c *Client) WalkTree(ctx context.Context, rootCID string, onProgress func(dirsSeen, filesSeen int), onDirError func(cid string, err error), onFile func(FileItem) error) error {
	const workers = 6

	visited := map[string]bool{rootCID: true}
	dirsSeen, filesSeen := 0, 0
	current := []string{rootCID}

	for len(current) > 0 {
		sem := make(chan struct{}, workers)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var next []string
		var firstErr error

		for _, cid := range current {
			wg.Add(1)
			sem <- struct{}{}
			go func(cid string) {
				defer wg.Done()
				defer func() { <-sem }()

				offset := 0
				for {
					items, err := c.listPage(ctx, cid, offset)
					if err != nil {
						if ctx.Err() != nil {
							mu.Lock()
							if firstErr == nil {
								firstErr = ctx.Err()
							}
							mu.Unlock()
							return
						}
						// Non-fatal: some 115 folders cannot be listed (HTTP 405 etc.). Log
						// and skip them so the rest of the tree still gets scanned.
						if onDirError != nil {
							onDirError(cid, err)
						}
						return
					}
					if len(items) == 0 {
						break
					}

					var subdirs []string
					var files []FileItem
					for _, it := range items {
						if it.IsDir {
							if it.FileID != "" {
								subdirs = append(subdirs, it.FileID)
							}
							continue
						}
						files = append(files, it)
					}

					mu.Lock()
					filesSeen += len(files)
					for _, d := range subdirs {
						if !visited[d] {
							visited[d] = true
							next = append(next, d)
						}
					}
					if onFile != nil {
						for _, f := range files {
							if err := onFile(f); err != nil && firstErr == nil {
								firstErr = err
							}
						}
					}
					mu.Unlock()

					offset += len(items)
					// The OpenAPI `count` field is unreliable for pagination; stop when a
					// short page arrives.
					if len(items) < 1000 {
						break
					}
				}

				mu.Lock()
				dirsSeen++
				ds, fs := dirsSeen, filesSeen
				mu.Unlock()
				if onProgress != nil && ds%50 == 0 {
					onProgress(ds, fs)
				}
			}(cid)
		}

		wg.Wait()
		if firstErr != nil {
			return firstErr
		}
		current = next
	}

	if onProgress != nil {
		onProgress(dirsSeen, filesSeen)
	}
	return nil
}

// listPage lists one page of a directory, preferring the cookie web API when a
// cookie is configured (the OpenAPI is WAF-protected for bulk listing).
func (c *Client) listPage(ctx context.Context, cid string, offset int) ([]FileItem, error) {
	if strings.TrimSpace(c.GetCookie()) != "" {
		itemss, _, err := c.ListFilesWeb(ctx, cid, 1000, offset, true)
		return itemss, err
	}
	return c.listWithRetry(ctx, cid, offset)
}

// listWithRetry lists one page, retrying on 115 rate-limit errors.
func (c *Client) listWithRetry(ctx context.Context, cid string, offset int) ([]FileItem, error) {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		items, _, err := c.ListFiles(ctx, cid, 1000, offset, true, "user_utime", 0)
		if err == nil {
			return items, nil
		}
		lastErr = err
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Category == ErrCatRateLimited {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(3 * time.Second):
				continue
			}
		}
		break
	}
	return nil, lastErr
}

// ListFilesRecursive lists all video files recursively in a tree using type=4&cur=0.
func (c *Client) ListFilesRecursive(ctx context.Context, cid string, limit, offset int) ([]FileItem, int, error) {
	if limit <= 0 {
		limit = 1150
	}

	query := url.Values{}
	query.Set("cid", cid)
	query.Set("type", "4") // 4 = videos
	query.Set("cur", "0")  // cur=0 triggers recursive subtree
	query.Set("limit", strconv.Itoa(limit))
	query.Set("offset", strconv.Itoa(offset))
	query.Set("o", "user_utime")
	query.Set("asc", "0")
	query.Set("custom_order", "1")
	query.Set("fc_mix", "0")
	query.Set("natsort", "1")

	endpoint := "/open/ufile/files"
	resp, err := c.DoRequest(ctx, "GET", endpoint, query, nil, "")
	if err != nil {
		return nil, 0, fmt.Errorf("list files recursive: %w", err)
	}

	// The OpenAPI returns either {"count":N,"data":[...]} or a bare array,
	// depending on the query (recursive listing returns an array).
	data := bytes.TrimSpace(resp.Data)
	if len(data) > 0 && data[0] == '[' {
		var rawItems []RawFileItem
		if err := json.Unmarshal(data, &rawItems); err != nil {
			return nil, 0, fmt.Errorf("unmarshal recursive files data (array): %w", err)
		}
		items := make([]FileItem, len(rawItems))
		for i, r := range rawItems {
			items[i] = r.Normalize()
		}
		return items, len(items), nil
	}

	var rawData struct {
		Count int           `json:"count"`
		Data  []RawFileItem `json:"data"`
	}
	if err := json.Unmarshal(data, &rawData); err != nil {
		return nil, 0, fmt.Errorf("unmarshal recursive files data: %w", err)
	}

	items := make([]FileItem, len(rawData.Data))
	for i, r := range rawData.Data {
		items[i] = r.Normalize()
	}

	return items, rawData.Count, nil
}

// GetDownloadURL retrieves a CDN direct download link using pick_code.
func (c *Client) GetDownloadURL(ctx context.Context, pickCode string) (*DownloadURLResponse, error) {
	endpoint := "/open/ufile/downurl"
	form := url.Values{}
	form.Set("pick_code", pickCode)

	resp, err := c.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return nil, fmt.Errorf("get download url: %w", err)
	}

	var dataMap map[string]struct {
		URL      json.RawMessage `json:"url"`
		FileSize int64           `json:"file_size"`
		FileName string          `json:"file_name"`
		FileID   string          `json:"file_id"`
		PickCode string          `json:"pick_code"`
	}
	if err := json.Unmarshal(resp.Data, &dataMap); err != nil {
		return nil, fmt.Errorf("unmarshal download url data: %w", err)
	}

	// `url` may be a plain string or a nested object {"url": "..."}.
	item := struct {
		URL      string
		FileSize int64
		FileName string
		FileID   string
		PickCode string
	}{}
	for _, v := range dataMap {
		item.FileSize = v.FileSize
		item.FileName = v.FileName
		item.FileID = v.FileID
		item.PickCode = v.PickCode
		if len(v.URL) > 0 {
			var s string
			if json.Unmarshal(v.URL, &s) == nil {
				item.URL = s
			} else {
				var nested struct {
					URL string `json:"url"`
				}
				if json.Unmarshal(v.URL, &nested) == nil {
					item.URL = nested.URL
				}
			}
		}
		break
	}

	if item.URL == "" {
		return nil, &APIError{
			Category: ErrCatNotFound,
			Message:  "download url is empty (file may have been deleted or is unavailable)",
		}
	}

	return &DownloadURLResponse{
		URL:        item.URL,
		PickCode:   pickCode,
		FileID:     item.FileID,
		FileName:   item.FileName,
		FileSize:   item.FileSize,
		ObtainedAt: time.Now().UTC(),
	}, nil
}

// CreateFolder creates a directory under parentID and returns the new folder's cid.
func (c *Client) CreateFolder(ctx context.Context, parentID, folderName string) (string, error) {
	endpoint := "/open/folder/add"
	form := url.Values{}
	form.Set("pid", parentID)
	form.Set("file_name", folderName)

	resp, err := c.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", fmt.Errorf("create folder: %w", err)
	}

	var res struct {
		CategoryID string `json:"file_id"`
		Cid        string `json:"cid"`
	}
	if err := json.Unmarshal(resp.Data, &res); err != nil {
		return "", fmt.Errorf("unmarshal create folder response: %w", err)
	}

	cid := res.Cid
	if cid == "" {
		cid = res.CategoryID
	}
	return cid, nil
}

func anyToString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return strconv.FormatInt(int64(val), 10)
	case int64:
		return strconv.FormatInt(val, 10)
	case int:
		return strconv.Itoa(val)
	}
	return fmt.Sprintf("%v", v)
}

func anyToInt64(v interface{}) int64 {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int64(val)
	case int64:
		return val
	case int:
		return int64(val)
	case string:
		n, _ := strconv.ParseInt(val, 10, 64)
		return n
	}
	return 0
}
