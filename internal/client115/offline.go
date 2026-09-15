package client115

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// OfflineTask represents a cloud download task in 115.
type OfflineTask struct {
	InfoHash      string  `json:"info_hash"`
	Name          string  `json:"name"`
	Size          int64   `json:"size"`
	PercentDone   float64 `json:"percent_done"`
	Status        int     `json:"status"` // -1: failed, 0: allocating, 1: downloading, 2: completed
	FileID        string  `json:"file_id"`
	URL           string  `json:"url"`
	PickCode      string  `json:"pick_code"`      // pick code of the downloaded file (ready to resolve a direct link)
	WpPathID      string  `json:"wp_path_id"`     // destination folder CID chosen when the task was created
	DelPath       string  `json:"del_path"`       // relative path / name shown in the cloud
	StatusText    string  `json:"status_text"`    // localized status (下载成功 / 下载失败 ...)
	DisplayStatus string  `json:"display_status"` // finished / failed / downloading ...
}

// RawOfflineTask handles heterogeneous task fields.
type RawOfflineTask struct {
	InfoHash      string      `json:"info_hash"`
	Name          string      `json:"name"`
	Size          interface{} `json:"size"`
	PercentDone   interface{} `json:"percentDone"`
	Status        int         `json:"status"`
	FileID        interface{} `json:"file_id"`
	URL           string      `json:"url"`
	PickCode      string      `json:"pick_code"`
	WpPathID      interface{} `json:"wp_path_id"`
	DelPath       string      `json:"del_path"`
	StatusText    string      `json:"status_text"`
	DisplayStatus string      `json:"display_status"`
}

func (r *RawOfflineTask) Normalize() OfflineTask {
	t := OfflineTask{
		InfoHash:      strings.ToUpper(r.InfoHash),
		Name:          r.Name,
		Size:          anyToInt64(r.Size),
		Status:        r.Status,
		FileID:        anyToString(r.FileID),
		URL:           r.URL,
		PickCode:      r.PickCode,
		WpPathID:      anyToString(r.WpPathID),
		DelPath:       r.DelPath,
		StatusText:    r.StatusText,
		DisplayStatus: r.DisplayStatus,
	}

	// In 115 API, status 4 means searching resources / allocating -> map to 0
	if t.Status == 4 {
		t.Status = 0
	}

	switch v := r.PercentDone.(type) {
	case float64:
		t.PercentDone = v
	case int:
		t.PercentDone = float64(v)
	case string:
		p, _ := strconv.ParseFloat(v, 64)
		t.PercentDone = p
	}

	return t
}

// AddBTTask submits a BT/magnet task. The 115 OpenAPI exposes a single
// add_task_urls endpoint; add_task_bt rejects requests, so a magnet URI is used.
func (c *Client) AddBTTask(ctx context.Context, infoHash, targetCID string) (string, error) {
	magnet := "magnet:?xt=urn:btih:" + strings.TrimSpace(infoHash)
	return c.AddURLTask(ctx, magnet, targetCID)
}

// AddURLTask submits a magnet/ED2K/HTTP task to the target folder.
// The OpenAPI expects a single `urls` parameter (multiple URLs separated by newlines).
func (c *Client) AddURLTask(ctx context.Context, resourceURL, targetCID string) (string, error) {
	endpoint := "/open/offline/add_task_urls"
	form := url.Values{}
	form.Set("urls", resourceURL)
	form.Set("wp_path_id", targetCID)

	resp, err := c.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", fmt.Errorf("add url task: %w", err)
	}

	// The OpenAPI returns a JSON array of per-url results.
	type addResult struct {
		State    bool   `json:"state"`
		Code     int    `json:"code"`
		Message  string `json:"message"`
		InfoHash string `json:"info_hash"`
		URL      string `json:"url"`
	}

	var results []addResult
	if err := json.Unmarshal(resp.Data, &results); err == nil && len(results) > 0 {
		if !results[0].State {
			return "", fmt.Errorf("115 添加任务失败(code=%d): %s", results[0].Code, results[0].Message)
		}
		return results[0].InfoHash, nil
	}

	// Fallback: some versions wrap the array in {"result": [...]}.
	var wrapped struct {
		Result []addResult `json:"result"`
	}
	if err := json.Unmarshal(resp.Data, &wrapped); err == nil && len(wrapped.Result) > 0 {
		if !wrapped.Result[0].State {
			return "", fmt.Errorf("115 添加任务失败(code=%d): %s", wrapped.Result[0].Code, wrapped.Result[0].Message)
		}
		return wrapped.Result[0].InfoHash, nil
	}

	return "", nil
}

// GetTaskList retrieves a page of offline tasks.
func (c *Client) GetTaskList(ctx context.Context, page int) ([]OfflineTask, int, error) {
	if page <= 0 {
		page = 1
	}

	query := url.Values{}
	query.Set("page", strconv.Itoa(page))

	endpoint := "/open/offline/get_task_list"
	resp, err := c.DoRequest(ctx, "GET", endpoint, query, nil, "")
	if err != nil {
		return nil, 0, fmt.Errorf("get offline task list: %w", err)
	}

	var rawData struct {
		Count int              `json:"count"`
		Tasks []RawOfflineTask `json:"tasks"`
	}
	if err := json.Unmarshal(resp.Data, &rawData); err != nil {
		return nil, 0, fmt.Errorf("unmarshal task list data: %w", err)
	}

	tasks := make([]OfflineTask, len(rawData.Tasks))
	for i, r := range rawData.Tasks {
		tasks[i] = r.Normalize()
	}

	return tasks, rawData.Count, nil
}

// FindTaskByInfoHash scans the offline task list (newest first) for a task
// matching the given info hash. 115 deduplicates magnet tasks globally, so a
// second add_task_urls for the same magnet returns 10008 ("任务已存在"); this
// lookup lets us recover the already-downloaded file instead of failing.
func (c *Client) FindTaskByInfoHash(ctx context.Context, infoHash string, maxPages int) (*OfflineTask, error) {
	infoHash = strings.ToUpper(strings.TrimSpace(infoHash))
	if infoHash == "" {
		return nil, nil
	}
	if maxPages <= 0 {
		maxPages = 8
	}
	for page := 1; page <= maxPages; page++ {
		tasks, _, err := c.GetTaskList(ctx, page)
		if err != nil {
			return nil, err
		}
		if len(tasks) == 0 {
			break
		}
		for i := range tasks {
			if strings.EqualFold(tasks[i].InfoHash, infoHash) {
				return &tasks[i], nil
			}
		}
	}
	return nil, nil
}

// DeleteTask removes an offline task by info_hash.
func (c *Client) DeleteTask(ctx context.Context, infoHash string, deleteSourceFile bool) error {
	endpoint := "/open/offline/del_task"
	form := url.Values{}
	form.Set("hash[0]", infoHash)
	if deleteSourceFile {
		form.Set("flag", "1")
	} else {
		form.Set("flag", "0")
	}

	_, err := c.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return fmt.Errorf("delete offline task: %w", err)
	}
	return nil
}
