package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	ErrAssetNotFound    = errors.New("matching release asset not found")
	ErrDownloadExceeded = errors.New("download size exceeded allowed archive budget")
)

type ReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ReleaseInfo struct {
	ID          int64          `json:"id"`
	TagName     string         `json:"tag_name"`
	Name        string         `json:"name"`
	CreatedAt   string         `json:"created_at"`
	PublishedAt string         `json:"published_at"`
	Assets      []ReleaseAsset `json:"assets"`
}

type DownloadedAsset struct {
	ReleaseID     string
	SourceName    string
	AssetName     string
	FilePath      string
	SHA256        string
	SizeBytes     int64
	CoverageStart *string
	CoverageEnd   *string
}

type Downloader struct {
	httpClient *http.Client
	apiURL     string
	mirrors    []string
	maxBytes   int64
	destDir    string
}

func NewDownloader(httpClient *http.Client, apiURL string, mirrors []string, maxBytes int64, destDir string) *Downloader {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	if apiURL == "" {
		apiURL = "https://api.github.com/repos/li-peifeng/AVdb-Only/releases/latest"
	}
	if maxBytes <= 0 {
		maxBytes = 512 * 1024 * 1024 // 512 MiB default
	}
	if destDir == "" {
		destDir = filepath.Join("data", "downloads")
	}
	return &Downloader{
		httpClient: httpClient,
		apiURL:     apiURL,
		mirrors:    mirrors,
		maxBytes:   maxBytes,
		destDir:    destDir,
	}
}

// FetchLatestRelease queries the GitHub release API.
func (d *Downloader) FetchLatestRelease(ctx context.Context) (*ReleaseInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", d.apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "MediaVault/1.0")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch latest release status: %d", resp.StatusCode)
	}

	var info ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("decode release json: %w", err)
	}

	return &info, nil
}

// DownloadAsset downloads a specific release asset with fallback mirrors and sha256 verification.
func (d *Downloader) DownloadAsset(ctx context.Context, release *ReleaseInfo, sourcePrefix string) (*DownloadedAsset, error) {
	if err := os.MkdirAll(d.destDir, 0755); err != nil {
		return nil, fmt.Errorf("create download dir: %w", err)
	}

	var targetAsset *ReleaseAsset
	for _, a := range release.Assets {
		if strings.HasPrefix(a.Name, sourcePrefix) && strings.HasSuffix(strings.ToLower(a.Name), ".zip") {
			targetAsset = &a
			break
		}
	}

	if targetAsset == nil {
		return nil, fmt.Errorf("%w: prefix=%s", ErrAssetNotFound, sourcePrefix)
	}

	destPath := filepath.Join(d.destDir, targetAsset.Name)

	// Try candidate URLs (original + mirrors)
	candidateURLs := []string{targetAsset.BrowserDownloadURL}
	for _, m := range d.mirrors {
		if m != "" {
			candidateURLs = append(candidateURLs, strings.TrimRight(m, "/")+"/"+targetAsset.BrowserDownloadURL)
		}
	}

	var lastErr error
	for _, candidateURL := range candidateURLs {
		sha256Hex, size, err := d.downloadToFile(ctx, candidateURL, destPath)
		if err == nil {
			covStart, covEnd := extractCoverage(targetAsset.Name, release.TagName)
			return &DownloadedAsset{
				ReleaseID:     release.TagName,
				SourceName:    sourcePrefix,
				AssetName:     targetAsset.Name,
				FilePath:      destPath,
				SHA256:        sha256Hex,
				SizeBytes:     size,
				CoverageStart: covStart,
				CoverageEnd:   covEnd,
			}, nil
		}
		lastErr = err
	}

	return nil, fmt.Errorf("all download candidates failed for %s: %w", targetAsset.Name, lastErr)
}

func (d *Downloader) downloadToFile(ctx context.Context, downloadURL, destPath string) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", "MediaVault/1.0")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("http status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return "", 0, fmt.Errorf("create file %s: %w", destPath, err)
	}
	defer out.Close()

	hasher := sha256.New()
	mw := io.MultiWriter(out, hasher)

	// Enforce archive_max_bytes budget
	limitReader := io.LimitReader(resp.Body, d.maxBytes+1)
	written, err := io.Copy(mw, limitReader)
	if err != nil {
		return "", 0, fmt.Errorf("write download data: %w", err)
	}

	if written > d.maxBytes {
		_ = os.Remove(destPath)
		return "", 0, fmt.Errorf("%w: downloaded %d exceeds limit %d", ErrDownloadExceeded, written, d.maxBytes)
	}

	sha256Hex := hex.EncodeToString(hasher.Sum(nil))
	return sha256Hex, written, nil
}

// extractCoverage attempts to parse date from filename or tag, e.g. "All_sehuatang_326097_2026-09-13-14-02-27.zip"
func extractCoverage(filename, tagName string) (*string, *string) {
	re := regexp.MustCompile(`(\d{4}-\d{2}-\d{2})`)
	matches := re.FindAllString(filename, -1)
	if len(matches) > 0 {
		date := matches[len(matches)-1]
		return nil, &date
	}
	if tagName != "" {
		m := re.FindString(tagName)
		if m != "" {
			return nil, &m
		}
	}
	return nil, nil
}

// BuildHTTPClientWithProxy creates an *http.Client with optional proxy URL.
func BuildHTTPClientWithProxy(proxyURL string, timeout time.Duration) (*http.Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
	}
	if proxyURL != "" {
		pu, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		transport.Proxy = http.ProxyURL(pu)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}
