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
	InfoHash    string  `json:"info_hash"`
	Name        string  `json:"name"`
	Size        int64   `json:"size"`
	PercentDone float64 `json:"percent_done"`
	Status      int     `json:"status"` // -1: failed, 0: allocating, 1: downloading, 2: completed
	FileID      string  `json:"file_id"`
	URL         string  `json:"url"`
}

// RawOfflineTask handles heterogeneous task fields.
type RawOfflineTask struct {
	InfoHash    string      `json:"info_hash"`
	Name        string      `json:"name"`
	Size        interface{} `json:"size"`
	PercentDone interface{} `json:"percentDone"`
	Status      int         `json:"status"`
	FileID      interface{} `json:"file_id"`
	URL         string      `json:"url"`
}

func (r *RawOfflineTask) Normalize() OfflineTask {
	t := OfflineTask{
		InfoHash: strings.ToUpper(r.InfoHash),
		Name:     r.Name,
		Size:     anyToInt64(r.Size),
		Status:   r.Status,
		FileID:   anyToString(r.FileID),
		URL:      r.URL,
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

// AddBTTask submits a BT task using info_hash and target folder cid.
func (c *Client) AddBTTask(ctx context.Context, infoHash, targetCID string) (string, error) {
	endpoint := "/open/offline/add_task_bt"
	form := url.Values{}
	form.Set("info_hash", strings.ToUpper(infoHash))
	form.Set("wp_path_id", targetCID)

	resp, err := c.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", fmt.Errorf("add bt task: %w", err)
	}

	var res struct {
		InfoHash string `json:"info_hash"`
	}
	_ = json.Unmarshal(resp.Data, &res)
	if res.InfoHash == "" {
		res.InfoHash = infoHash
	}
	return res.InfoHash, nil
}

// AddURLTask submits an ED2K or HTTP task to target folder cid.
func (c *Client) AddURLTask(ctx context.Context, resourceURL, targetCID string) (string, error) {
	endpoint := "/open/offline/add_task_urls"
	form := url.Values{}
	form.Set("url[0]", resourceURL)
	form.Set("wp_path_id", targetCID)

	resp, err := c.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return "", fmt.Errorf("add url task: %w", err)
	}

	var res struct {
		Result []struct {
			InfoHash string `json:"info_hash"`
			URL      string `json:"url"`
			State    bool   `json:"state"`
			ErrCode  int    `json:"errcode"`
		} `json:"result"`
	}
	_ = json.Unmarshal(resp.Data, &res)
	if len(res.Result) > 0 && res.Result[0].InfoHash != "" {
		return res.Result[0].InfoHash, nil
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
