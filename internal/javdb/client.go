package javdb

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL         = "https://jdforrepam.com"
	DefaultHost            = "jdforrepam.com"
	JavDBSignaturePrefix   = "lpw6vgqzsp"
	JavDBSignatureSecret   = "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
)

var (
	ErrNotFound       = errors.New("javdb movie not found")
	ErrRiskControl    = errors.New("javdb risk control triggered (429/403)")
	ErrTransient      = errors.New("javdb transient server error")
	ErrCircuitOpen    = errors.New("javdb circuit breaker is open")
	ErrContractChange = errors.New("javdb api parsing contract changed")

	magnetRegex = regexp.MustCompile(`(?i)magnet:\?[^ \t\r\n<>"'\[\]{}]+`)
	ed2kRegex   = regexp.MustCompile(`(?i)ed2k://\|(?:file|folder)\|[^|]+\|\d+\|[0-9a-fA-F]{32}\|/`)
)

type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

type CircuitBreaker struct {
	mu           sync.Mutex
	state        CircuitState
	failCount    int
	cooldown     time.Duration
	openedAt     time.Time
	halfOpenBusy bool
}

func NewCircuitBreaker(cooldown time.Duration) *CircuitBreaker {
	if cooldown <= 0 {
		cooldown = 30 * time.Minute
	}
	return &CircuitBreaker{cooldown: cooldown}
}

func (cb *CircuitBreaker) Allow() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	now := time.Now()
	switch cb.state {
	case CircuitClosed:
		return nil
	case CircuitOpen:
		if now.Sub(cb.openedAt) >= cb.cooldown {
			cb.state = CircuitHalfOpen
			cb.halfOpenBusy = true
			return nil
		}
		return ErrCircuitOpen
	case CircuitHalfOpen:
		if cb.halfOpenBusy {
			return ErrCircuitOpen
		}
		cb.halfOpenBusy = true
		return nil
	default:
		return nil
	}
}

func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = CircuitClosed
	cb.failCount = 0
	cb.halfOpenBusy = false
}

func (cb *CircuitBreaker) RecordFailure(isRiskControl bool) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if !isRiskControl {
		cb.halfOpenBusy = false
		return
	}

	cb.failCount++
	if cb.state == CircuitHalfOpen || cb.failCount >= 3 {
		cb.state = CircuitOpen
		cb.openedAt = time.Now()
		cb.halfOpenBusy = false
	}
}

// ClientConfig holds configuration for JavDB API client.
type ClientConfig struct {
	BaseURL            string
	Host               string
	RequestIntervalSec float64 // default 3.0
	DelayMin           float64 // default 2.5
	DelayMax           float64 // default 4.5
	Concurrency        int     // default 2
	CircuitCooldown    time.Duration
	HTTPClient         *http.Client
}

type Client struct {
	cfg        ClientConfig
	httpClient *http.Client
	cb         *CircuitBreaker
	sem        chan struct{}
	rateMu     sync.Mutex
	lastReqAt  time.Time
}

func NewClient(cfg ClientConfig) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.Host == "" {
		cfg.Host = DefaultHost
	}
	if cfg.RequestIntervalSec <= 0 {
		cfg.RequestIntervalSec = 3.0
	}
	if cfg.DelayMin <= 0 {
		cfg.DelayMin = 2.5
	}
	if cfg.DelayMax <= 0 || cfg.DelayMax < cfg.DelayMin {
		cfg.DelayMax = 4.5
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 2
	}
	if cfg.CircuitCooldown <= 0 {
		cfg.CircuitCooldown = 30 * time.Minute
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}

	return &Client{
		cfg:        cfg,
		httpClient: cfg.HTTPClient,
		cb:         NewCircuitBreaker(cfg.CircuitCooldown),
		sem:        make(chan struct{}, cfg.Concurrency),
	}
}

func BuildSignature() string {
	timestamp := time.Now().Unix()
	toHash := fmt.Sprintf("%d%s", timestamp, JavDBSignatureSecret)
	digest := md5.Sum([]byte(toHash))
	return fmt.Sprintf("%d.%s.%x", timestamp, JavDBSignaturePrefix, digest)
}

func (c *Client) acquire(ctx context.Context) error {
	// 1. Check circuit breaker
	if err := c.cb.Allow(); err != nil {
		return err
	}

	// 2. Concurrency semaphore
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.sem <- struct{}{}:
	}

	// 3. Rate limiter & randomized jitter
	c.rateMu.Lock()
	now := time.Now()
	interval := time.Duration(c.cfg.RequestIntervalSec * float64(time.Second))
	elapsed := now.Sub(c.lastReqAt)
	if elapsed < interval {
		time.Sleep(interval - elapsed)
	}

	// Add randomized delay
	jitterRange := c.cfg.DelayMax - c.cfg.DelayMin
	if jitterRange > 0 {
		jitter := c.cfg.DelayMin + rand.Float64()*jitterRange
		time.Sleep(time.Duration(jitter * float64(time.Second)))
	}
	c.lastReqAt = time.Now()
	c.rateMu.Unlock()

	return nil
}

func (c *Client) release() {
	select {
	case <-c.sem:
	default:
	}
}

func (c *Client) doRequest(ctx context.Context, path string, query url.Values) ([]byte, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	fullURL := strings.TrimRight(c.cfg.BaseURL, "/") + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create javdb request: %w", err)
	}

	req.Header.Set("User-Agent", "Dart/3.5 (dart:io)")
	req.Header.Set("Accept-Language", "zh-TW")
	req.Header.Set("Host", c.cfg.Host)
	req.Header.Set("jdSignature", BuildSignature())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.cb.RecordFailure(false)
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
		c.cb.RecordFailure(true)
		return nil, fmt.Errorf("%w: http status %d", ErrRiskControl, resp.StatusCode)
	}

	if resp.StatusCode == http.StatusNotFound {
		c.cb.RecordSuccess()
		return nil, ErrNotFound
	}

	if resp.StatusCode >= 500 {
		c.cb.RecordFailure(false)
		return nil, fmt.Errorf("%w: http status %d", ErrTransient, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.cb.RecordFailure(false)
		return nil, fmt.Errorf("read response body: %w", err)
	}

	var baseResp struct {
		Success bool            `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &baseResp); err != nil {
		c.cb.RecordFailure(false)
		return nil, fmt.Errorf("%w: unmarshal response: %v", ErrContractChange, err)
	}

	if !baseResp.Success {
		if strings.Contains(strings.ToLower(baseResp.Message), "not found") {
			c.cb.RecordSuccess()
			return nil, ErrNotFound
		}
		c.cb.RecordFailure(false)
		return nil, fmt.Errorf("javdb error: %s", baseResp.Message)
	}

	c.cb.RecordSuccess()
	return baseResp.Data, nil
}

// SearchMovie searches JavDB for a code and returns the best matching movie.
func (c *Client) SearchMovie(ctx context.Context, number string) (*MovieDetailDTO, error) {
	query := url.Values{}
	query.Set("q", strings.TrimSpace(number))
	query.Set("type", "movie")
	query.Set("movie_type", "all")
	query.Set("page", "1")
	query.Set("limit", "5")

	dataBytes, err := c.doRequest(ctx, "/api/v2/search", query)
	if err != nil {
		return nil, err
	}

	var searchData struct {
		Movies []struct {
			ID            string   `json:"id"`
			Number        string   `json:"number"`
			Title         string   `json:"title"`
			CoverURL      *string  `json:"cover_url"`
			PosterURL     *string  `json:"poster_url"`
			Score         *float64 `json:"score"`
			ReleaseDate   *string  `json:"release_date"`
			Runtime       *int     `json:"runtime"`
			Actors        []string `json:"actors"`
			Tags          []string `json:"tags"`
			Maker         *string  `json:"maker"`
			Director      *string  `json:"director"`
			VideoType     string   `json:"video_type"`
		} `json:"movies"`
	}

	if err := json.Unmarshal(dataBytes, &searchData); err != nil {
		return nil, fmt.Errorf("%w: decode search result: %v", ErrContractChange, err)
	}

	if len(searchData.Movies) == 0 {
		return nil, ErrNotFound
	}

	// Exact code matching
	cleanTarget := cleanCode(number)
	var matched *MovieDetailDTO
	for _, m := range searchData.Movies {
		if cleanCode(m.Number) == cleanTarget {
			dto := &MovieDetailDTO{
				ID:             m.ID,
				Number:         m.Number,
				Title:          m.Title,
				CoverURL:       m.CoverURL,
				PosterURL:      m.PosterURL,
				Score:          m.Score,
				ReleaseDate:    m.ReleaseDate,
				RuntimeSeconds: m.Runtime,
				Actors:         m.Actors,
				Tags:           m.Tags,
				Maker:          m.Maker,
				Director:       m.Director,
				VideoType:      m.VideoType,
			}
			matched = dto
			break
		}
	}

	if matched == nil {
		// Fallback to first if closely resembles
		first := searchData.Movies[0]
		matched = &MovieDetailDTO{
			ID:             first.ID,
			Number:         first.Number,
			Title:          first.Title,
			CoverURL:       first.CoverURL,
			PosterURL:      first.PosterURL,
			Score:          first.Score,
			ReleaseDate:    first.ReleaseDate,
			RuntimeSeconds: first.Runtime,
			Actors:         first.Actors,
			Tags:           first.Tags,
			Maker:          first.Maker,
			Director:       first.Director,
			VideoType:      first.VideoType,
		}
	}

	return matched, nil
}

// GetMovieDetail retrieves complete movie details by JavDB ID.
func (c *Client) GetMovieDetail(ctx context.Context, id string) (*MovieDetailDTO, error) {
	dataBytes, err := c.doRequest(ctx, "/api/v1/movies/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}

	var detail struct {
		ID            string   `json:"id"`
		Number        string   `json:"number"`
		Title         string   `json:"title"`
		TitleZH       *string  `json:"title_zh"`
		DescriptionZH *string  `json:"description_zh"`
		CoverURL      *string  `json:"cover_url"`
		PosterURL     *string  `json:"poster_url"`
		Score         *float64 `json:"score"`
		ReleaseDate   *string  `json:"release_date"`
		Runtime       *int     `json:"runtime"`
		Actors        []string `json:"actors"`
		Tags          []string `json:"tags"`
		Maker         *string  `json:"maker"`
		Director      *string  `json:"director"`
		VideoType     string   `json:"video_type"`
	}

	if err := json.Unmarshal(dataBytes, &detail); err != nil {
		return nil, fmt.Errorf("%w: decode movie detail: %v", ErrContractChange, err)
	}

	return &MovieDetailDTO{
		ID:             detail.ID,
		Number:         detail.Number,
		Title:          detail.Title,
		TitleZH:        detail.TitleZH,
		DescriptionZH:  detail.DescriptionZH,
		CoverURL:       detail.CoverURL,
		PosterURL:      detail.PosterURL,
		Score:          detail.Score,
		ReleaseDate:    detail.ReleaseDate,
		RuntimeSeconds: detail.Runtime,
		Actors:         detail.Actors,
		Tags:           detail.Tags,
		Maker:          detail.Maker,
		Director:       detail.Director,
		VideoType:      detail.VideoType,
	}, nil
}

// GetOfficialMagnets retrieves magnets from JavDB.
func (c *Client) GetOfficialMagnets(ctx context.Context, id string) ([]MagnetDTO, error) {
	dataBytes, err := c.doRequest(ctx, "/api/v1/movies/"+url.PathEscape(id)+"/magnets", nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Magnets []struct {
			Name      string `json:"name"`
			MagnetURL string `json:"magnet_url"`
			SizeBytes int64  `json:"size_bytes"`
			SizeMB    int64  `json:"size_mb"`
			HasHD     bool   `json:"has_hd"`
			HasSub    bool   `json:"has_sub"`
			Seeders   int    `json:"seeders"`
		} `json:"magnets"`
	}

	if err := json.Unmarshal(dataBytes, &result); err != nil {
		return nil, fmt.Errorf("%w: decode magnets: %v", ErrContractChange, err)
	}

	var list []MagnetDTO
	for _, m := range result.Magnets {
		size := m.SizeBytes
		if size == 0 && m.SizeMB > 0 {
			size = m.SizeMB * 1024 * 1024
		}
		list = append(list, MagnetDTO{
			Name:      m.Name,
			MagnetURL: m.MagnetURL,
			SizeBytes: size,
			HasHD:     m.HasHD,
			HasSub:    m.HasSub,
			Seeders:   m.Seeders,
		})
	}

	return list, nil
}

// GetReviewLinks retrieves hidden magnet / ed2k links shared in reviews.
func (c *Client) GetReviewLinks(ctx context.Context, id string) ([]string, error) {
	query := url.Values{}
	query.Set("page", "1")
	query.Set("limit", "20")

	dataBytes, err := c.doRequest(ctx, "/api/v1/movies/"+url.PathEscape(id)+"/reviews", query)
	if err != nil {
		return nil, err
	}

	var result struct {
		Reviews []struct {
			Content string `json:"content"`
		} `json:"reviews"`
	}

	if err := json.Unmarshal(dataBytes, &result); err != nil {
		return nil, fmt.Errorf("%w: decode reviews: %v", ErrContractChange, err)
	}

	var links []string
	seen := make(map[string]bool)

	for _, r := range result.Reviews {
		content := r.Content
		for i := 0; i < 3; i++ {
			content = html.UnescapeString(content)
		}

		for _, m := range magnetRegex.FindAllString(content, -1) {
			clean := strings.TrimRight(m, ".,;:!?)]}'>\"，。；：！？）】》")
			if strings.Contains(strings.ToLower(clean), "xt=urn:btih:") && !seen[clean] {
				seen[clean] = true
				links = append(links, clean)
			}
		}

		for _, ed := range ed2kRegex.FindAllString(content, -1) {
			clean := strings.TrimRight(ed, ".,;:!?)]}'>\"，。；：！？）】》")
			if !seen[clean] {
				seen[clean] = true
				links = append(links, clean)
			}
		}
	}

	return links, nil
}

// GetRankings retrieves daily, weekly, or monthly rankings.
func (c *Client) GetRankings(ctx context.Context, period string, rankingType string) ([]RankingMovieDTO, error) {
	query := url.Values{}
	query.Set("period", period)
	query.Set("type", rankingType)

	dataBytes, err := c.doRequest(ctx, "/api/v1/rankings", query)
	if err != nil {
		return nil, err
	}

	var result struct {
		Movies []struct {
			ID          string   `json:"id"`
			Number      string   `json:"number"`
			Title       string   `json:"title"`
			CoverURL    *string  `json:"cover_url"`
			Score       *float64 `json:"score"`
			ReleaseDate *string  `json:"release_date"`
			VideoType   string   `json:"video_type"`
		} `json:"movies"`
	}

	if err := json.Unmarshal(dataBytes, &result); err != nil {
		return nil, fmt.Errorf("%w: decode rankings: %v", ErrContractChange, err)
	}

	var list []RankingMovieDTO
	for idx, m := range result.Movies {
		list = append(list, RankingMovieDTO{
			ID:          m.ID,
			Number:      m.Number,
			Title:       m.Title,
			CoverURL:    m.CoverURL,
			Score:       m.Score,
			ReleaseDate: m.ReleaseDate,
			Rank:        idx + 1,
			VideoType:   m.VideoType,
		})
	}

	return list, nil
}

// GetTop250Page retrieves a page of TOP 250 movies.
func (c *Client) GetTop250Page(ctx context.Context, startRank int, movieType, year string) ([]RankingMovieDTO, error) {
	query := url.Values{}
	query.Set("start_rank", strconv.Itoa(startRank))
	query.Set("ignore_watched", "false")
	query.Set("page", "1")
	query.Set("limit", "50")

	if year != "" {
		query.Set("type", "year")
		query.Set("type_value", year)
	} else if movieType != "" && movieType != "all" {
		query.Set("type", "video_type")
		query.Set("type_value", movieType)
	} else {
		query.Set("type", "all")
	}

	dataBytes, err := c.doRequest(ctx, "/api/v1/movies/top", query)
	if err != nil {
		return nil, err
	}

	var result struct {
		Movies []struct {
			ID          string   `json:"id"`
			Number      string   `json:"number"`
			Title       string   `json:"title"`
			CoverURL    *string  `json:"cover_url"`
			Score       *float64 `json:"score"`
			ReleaseDate *string  `json:"release_date"`
			VideoType   string   `json:"video_type"`
		} `json:"movies"`
	}

	if err := json.Unmarshal(dataBytes, &result); err != nil {
		return nil, fmt.Errorf("%w: decode top250: %v", ErrContractChange, err)
	}

	var list []RankingMovieDTO
	for idx, m := range result.Movies {
		list = append(list, RankingMovieDTO{
			ID:          m.ID,
			Number:      m.Number,
			Title:       m.Title,
			CoverURL:    m.CoverURL,
			Score:       m.Score,
			ReleaseDate: m.ReleaseDate,
			Rank:        startRank + idx,
			VideoType:   m.VideoType,
		})
	}

	return list, nil
}

func cleanCode(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
