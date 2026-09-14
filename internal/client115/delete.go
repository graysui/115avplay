package client115

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// DeleteFiles deletes specific files or directories by file ID.
func (c *Client) DeleteFiles(ctx context.Context, fileIDs []string) error {
	if len(fileIDs) == 0 {
		return nil
	}

	endpoint := "/open/ufile/delete"
	form := url.Values{}
	for _, id := range fileIDs {
		form.Add("file_ids[]", id)
	}

	_, err := c.DoRequest(ctx, "POST", endpoint, nil, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
	if err != nil {
		return fmt.Errorf("delete files: %w", err)
	}
	return nil
}

// ConfirmMissing verifies that a file or directory no longer exists in 115.
func (c *Client) ConfirmMissing(ctx context.Context, parentCID, fileID string) (bool, error) {
	items, _, err := c.ListFiles(ctx, parentCID, 100, 0, true, "user_utime", 0)
	if err != nil {
		if apiErr, ok := err.(*APIError); ok && apiErr.Category == ErrCatNotFound {
			// Parent directory doesn't even exist, so file is missing
			return true, nil
		}
		return false, err
	}

	for _, item := range items {
		if item.FileID == fileID {
			// Still exists
			return false, nil
		}
	}

	return true, nil
}
