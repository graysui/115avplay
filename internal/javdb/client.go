package javdb

import (
	"bytes"
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

	"github.com/google/uuid"
)

const (
	DefaultBaseURL       = "https://jdforrepam.com"
	DefaultHost          = "jdforrepam.com"
	JavDBSignaturePrefix = "lpw6vgqzsp"
	JavDBSignatureSecret = "71cf27bb3c0bcdf207b64abecddc970098c7421ee7203b9cdae54478478a199e7d5a6e1a57691123c1a931c057842fb73ba3b3c83bcd69c17ccf174081e3d8aa"
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
	Token              string  // optional JavDB account token (Authorization: Bearer)
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
	tokenMu    sync.RWMutex
	token      string
	cookieMu   sync.RWMutex
	cookies    map[string]string
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
		token:      cfg.Token,
		cookies:    map[string]string{},
	}
}

// SetCookie loads a raw "k=v; k2=v2" cookie string into the client's jar.
func (c *Client) SetCookie(raw string) {
	c.cookieMu.Lock()
	defer c.cookieMu.Unlock()
	c.cookies = map[string]string{}
	for _, part := range strings.Split(raw, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 && kv[0] != "" {
			c.cookies[kv[0]] = kv[1]
		}
	}
}

// GetCookie returns the current cookie jar as a "k=v; k2=v2" string.
func (c *Client) GetCookie() string {
	c.cookieMu.RLock()
	defer c.cookieMu.RUnlock()
	if len(c.cookies) == 0 {
		return ""
	}
	parts := make([]string, 0, len(c.cookies))
	for k, v := range c.cookies {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// mergeSetCookies records Set-Cookie response headers into the jar.
func (c *Client) mergeSetCookies(setCookies []string) {
	if len(setCookies) == 0 {
		return
	}
	c.cookieMu.Lock()
	defer c.cookieMu.Unlock()
	if c.cookies == nil {
		c.cookies = map[string]string{}
	}
	for _, sc := range setCookies {
		kv := strings.SplitN(strings.SplitN(sc, ";", 2)[0], "=", 2)
		if len(kv) == 2 && strings.TrimSpace(kv[0]) != "" {
			c.cookies[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
}

// SetToken updates the JavDB account token used for authenticated requests.
func (c *Client) SetToken(token string) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.token = token
}

// GetToken returns the currently configured JavDB account token.
func (c *Client) GetToken() string {
	c.tokenMu.RLock()
	defer c.tokenMu.RUnlock()
	return c.token
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
	return c.doRequestWithBody(ctx, "GET", path, query, nil, "")
}

func (c *Client) doRequestWithBody(ctx context.Context, method, path string, query url.Values, reqBody io.Reader, contentType string) ([]byte, error) {
	if err := c.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.release()

	fullURL := strings.TrimRight(c.cfg.BaseURL, "/") + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return nil, fmt.Errorf("create javdb request: %w", err)
	}

	req.Header.Set("User-Agent", "Dart/3.5 (dart:io)")
	req.Header.Set("Accept-Language", "zh-TW")
	req.Header.Set("Host", c.cfg.Host)
	req.Header.Set("jdSignature", BuildSignature())
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token := c.GetToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	if cookie := c.GetCookie(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.cb.RecordFailure(false)
		return nil, fmt.Errorf("%w: %v", ErrTransient, err)
	}
	defer resp.Body.Close()

	// Capture session cookies returned by JavDB (login/refresh).
	c.mergeSetCookies(resp.Header.Values("Set-Cookie"))

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
		Success json.RawMessage `json:"success"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &baseResp); err != nil {
		c.cb.RecordFailure(false)
		return nil, fmt.Errorf("%w: unmarshal response: %v", ErrContractChange, err)
	}

	if !jsonBool(baseResp.Success) {
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
			ID          string   `json:"id"`
			Number      string   `json:"number"`
			Title       string   `json:"title"`
			CoverURL    *string  `json:"cover_url"`
			PosterURL   *string  `json:"poster_url"`
			Score       *float64 `json:"score"`
			ReleaseDate *string  `json:"release_date"`
			Runtime     *int     `json:"runtime"`
			Actors      []string `json:"actors"`
			Tags        []string `json:"tags"`
			Maker       *string  `json:"maker"`
			Director    *string  `json:"director"`
			VideoType   string   `json:"video_type"`
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
				CoverURL:       normalizeImagePtr(m.CoverURL),
				PosterURL:      normalizeImagePtr(m.PosterURL),
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
			CoverURL:       normalizeImagePtr(first.CoverURL),
			PosterURL:      normalizeImagePtr(first.PosterURL),
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
	dataBytes, err := c.doRequest(ctx, "/api/v2/movies/"+url.PathEscape(id), nil)
	if err != nil {
		return nil, err
	}

	var detail struct {
		Movie struct {
			ID            string          `json:"id"`
			Number        string          `json:"number"`
			Title         string          `json:"title"`
			TitleZH       *string         `json:"title_zh"`
			DescriptionZH *string         `json:"description_zh"`
			CoverURL      *string         `json:"cover_url"`
			PosterURL     *string         `json:"poster_url"`
			Score         json.RawMessage `json:"score"`
			ReleaseDate   *string         `json:"release_date"`
			Duration      *int            `json:"duration"` // minutes
			Type          json.RawMessage `json:"type"`
			MakerName     *string         `json:"maker_name"`
			DirectorName  *string         `json:"director_name"`
			Actors        []struct {
				Name string `json:"name"`
			} `json:"actors"`
			Tags []struct {
				Name string `json:"name"`
			} `json:"tags"`
		} `json:"movie"`
	}

	if err := json.Unmarshal(dataBytes, &detail); err != nil {
		return nil, fmt.Errorf("%w: decode movie detail: %v", ErrContractChange, err)
	}

	m := detail.Movie
	actors := make([]string, 0, len(m.Actors))
	for _, a := range m.Actors {
		if a.Name != "" {
			actors = append(actors, a.Name)
		}
	}
	tags := make([]string, 0, len(m.Tags))
	for _, t := range m.Tags {
		if t.Name != "" {
			tags = append(tags, t.Name)
		}
	}

	var runtimeSeconds *int
	if m.Duration != nil && *m.Duration > 0 {
		rs := *m.Duration * 60
		runtimeSeconds = &rs
	}

	return &MovieDetailDTO{
		ID:             m.ID,
		Number:         m.Number,
		Title:          m.Title,
		TitleZH:        m.TitleZH,
		DescriptionZH:  m.DescriptionZH,
		CoverURL:       normalizeImagePtr(m.CoverURL),
		PosterURL:      normalizeImagePtr(m.PosterURL),
		Score:          jsonNumber(m.Score),
		ReleaseDate:    m.ReleaseDate,
		RuntimeSeconds: runtimeSeconds,
		Actors:         actors,
		Tags:           tags,
		Maker:          m.MakerName,
		Director:       m.DirectorName,
		VideoType:      jsonString(m.Type),
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
			Name   string `json:"name"`
			Hash   string `json:"hash"`
			SizeMB int64  `json:"size"`
			HD     bool   `json:"hd"`
			CNSub  bool   `json:"cnsub"`
		} `json:"magnets"`
	}

	if err := json.Unmarshal(dataBytes, &result); err != nil {
		return nil, fmt.Errorf("%w: decode magnets: %v", ErrContractChange, err)
	}

	var list []MagnetDTO
	for _, m := range result.Magnets {
		magnetURL := ""
		if m.Hash != "" {
			magnetURL = "magnet:?xt=urn:btih:" + m.Hash
		}
		list = append(list, MagnetDTO{
			Name:      m.Name,
			MagnetURL: magnetURL,
			SizeBytes: m.SizeMB * 1024 * 1024,
			HasHD:     m.HD,
			HasSub:    m.CNSub,
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

// NormalizeImageURL rewrites JavDB app-CDN image URLs (tp.spfcas.com, which serves
// encrypted payloads to non-app clients) to the public web CDN (c0.jdbstatic.com)
// that returns standard JPEGs.
func NormalizeImageURL(raw string) string {
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if !strings.HasSuffix(strings.ToLower(u.Hostname()), "spfcas.com") {
		return raw
	}
	// Path looks like /rhe951l4q/covers/0e/xxxx.jpg -> /covers/0e/xxxx.jpg
	parts := strings.SplitN(strings.TrimPrefix(u.Path, "/"), "/", 2)
	if len(parts) != 2 {
		return raw
	}
	path := "/" + parts[1]
	path = strings.Replace(path, "/small_covers/", "/covers/", 1)
	return "https://c0.jdbstatic.com" + path
}

func normalizeImagePtr(p *string) *string {
	if p == nil || *p == "" {
		return p
	}
	n := NormalizeImageURL(*p)
	if n == *p {
		return p
	}
	return &n
}

// LoginResult contains the account identity returned by a successful login.
type LoginResult struct {
	Token    string
	UserID   string
	Username string
}

// Login authenticates with a JavDB username/password and stores the resulting
// bearer token on the client. The /api/v1/sessions endpoint requires a set of
// device identification fields in addition to the credentials.
func (c *Client) Login(ctx context.Context, username, password string) (*LoginResult, error) {
	payload := map[string]string{
		"device_uuid":        uuid.NewString(),
		"device_name":        "MediaVault",
		"device_model":       "Server",
		"platform":           "linux",
		"system_version":     "MediaVault/1.0",
		"app_version":        "1.0.0",
		"app_version_number": "1",
		"app_channel":        "official",
		"username":           strings.TrimSpace(username),
		"password":           password,
	}
	body, _ := json.Marshal(payload)

	dataBytes, err := c.doRequestWithBody(ctx, "POST", "/api/v1/sessions", nil, bytes.NewReader(body), "application/json")
	if err != nil {
		return nil, err
	}

	var data map[string]interface{}
	if err := json.Unmarshal(dataBytes, &data); err != nil {
		return nil, fmt.Errorf("%w: decode login response: %v", ErrContractChange, err)
	}

	token := findString(data, "token", "access_token", "api_token", "auth_token", "jwt")
	if token == "" {
		return nil, fmt.Errorf("%w: login response did not contain a token", ErrContractChange)
	}

	res := &LoginResult{
		Token:    token,
		UserID:   findString(data, "id", "user_id", "uid"),
		Username: findString(data, "username", "name", "nickname"),
	}
	c.SetToken(token)
	return res, nil
}

// findString searches a decoded JSON object (including a nested "user" object) for
// the first non-empty value among the given keys.
func findString(data map[string]interface{}, keys ...string) string {
	scopes := []map[string]interface{}{data}
	for _, nested := range []string{"user", "data", "account"} {
		if m, ok := data[nested].(map[string]interface{}); ok {
			scopes = append(scopes, m)
		}
	}
	for _, scope := range scopes {
		for _, k := range keys {
			if v, ok := scope[k]; ok {
				switch t := v.(type) {
				case string:
					if strings.TrimSpace(t) != "" {
						return t
					}
				case float64:
					return strconv.FormatInt(int64(t), 10)
				}
			}
		}
	}
	return ""
}

// jsonBool interprets a JSON value that may be a bool, number or string, as found
// in JavDB responses ("success": 1 / true / "1").
func jsonBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n != 0
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		s = strings.ToLower(strings.TrimSpace(s))
		return s == "1" || s == "true"
	}
	return false
}

// jsonString parses a JSON value that may be a string, number or null into a string.
func jsonString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

// jsonNumber parses a JSON value that may be a number or a numeric string.
func jsonNumber(raw json.RawMessage) *float64 {
	if len(raw) == 0 {
		return nil
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return &f
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return &parsed
		}
	}
	return nil
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
