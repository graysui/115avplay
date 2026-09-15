package emby

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mediavault/internal/db"
	"mediavault/internal/javdb"
)

// 1x1 transparent GIF as placeholder if image unavailable
var transparentGIF = []byte{
	0x47, 0x49, 0x46, 0x38, 0x39, 0x61, 0x01, 0x00, 0x01, 0x00,
	0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff, 0xff, 0xff, 0x21,
	0xf9, 0x04, 0x01, 0x00, 0x00, 0x00, 0x00, 0x2c, 0x00, 0x00,
	0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x02, 0x01, 0x44,
	0x00, 0x3b,
}

type ImageHandlers struct {
	cacheDir   string
	movieRepo  *db.MovieRepo
	httpClient *http.Client
}

func NewImageHandlers(cacheDir string, movieRepo *db.MovieRepo) *ImageHandlers {
	if cacheDir == "" {
		cacheDir = filepath.Join("data", "cache", "images")
	}
	_ = os.MkdirAll(cacheDir, 0755)

	return &ImageHandlers{
		cacheDir:  cacheDir,
		movieRepo: movieRepo,
		httpClient: &http.Client{
			Timeout: 6 * time.Second,
		},
	}
}

// ServeItemImage handles GET /items/{id}/images/{type} and /items/{id}/images/{type}/{index}.
func (h *ImageHandlers) ServeItemImage(w http.ResponseWriter, r *http.Request, rawID, imageType string) {
	movieCode, err := DecodeItemID(rawID)
	if err != nil {
		http.Error(w, "invalid item id", http.StatusNotFound)
		return
	}

	movie, err := h.movieRepo.GetMovie(r.Context(), movieCode)
	if err != nil || movie == nil {
		http.Error(w, "item not found", http.StatusNotFound)
		return
	}

	imageTypeLower := strings.ToLower(imageType)
	previews := parsePreviewImages(movie.PreviewImages)

	// Build an ordered list of candidate image URLs (JavDB cover first, then the
	// offline preview images as fallback for dead/blocked hosts).
	var candidates []string
	addCandidate := func(u string) {
		u = strings.TrimSpace(u)
		if u == "" || !strings.HasPrefix(u, "http") {
			return
		}
		u = javdb.NormalizeImageURL(u)
		for _, c := range candidates {
			if c == u {
				return
			}
		}
		candidates = append(candidates, u)
	}

	if imageTypeLower == "primary" || imageTypeLower == "thumb" {
		if movie.CoverURL != nil {
			addCandidate(*movie.CoverURL)
		}
		if movie.PosterURL != nil {
			addCandidate(*movie.PosterURL)
		}
		for _, p := range previews {
			addCandidate(p)
		}
	} else if imageTypeLower == "backdrop" {
		for _, p := range previews {
			addCandidate(p)
		}
		if movie.CoverURL != nil {
			addCandidate(*movie.CoverURL)
		}
	}

	if len(candidates) == 0 {
		writeImagePlaceholder(w, r)
		return
	}

	// Cap how many candidates we try so latency stays bounded.
	if len(candidates) > 4 {
		candidates = candidates[:4]
	}

	// Serve from disk cache when any candidate is already cached.
	for _, u := range candidates {
		etag := imageETag(u)
		cachePath := filepath.Join(h.cacheDir, etag)
		if fi, err := os.Stat(cachePath); err == nil && fi.Size() > 0 {
			content, err := os.ReadFile(cachePath)
			if err != nil {
				continue
			}
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", "public, max-age=604800")
			w.Header().Set("Content-Type", detectContentType(cachePath, content))
			_, _ = w.Write(content)
			return
		}
	}

	// Race the candidates in parallel with a short overall deadline: this avoids
	// waiting on dead hosts and picks whichever mirror responds first.
	fetchCtx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	type fetchResult struct {
		body []byte
		url  string
	}
	results := make(chan fetchResult, len(candidates))
	for _, u := range candidates {
		go func(u string) {
			body, ok := h.fetchImage(fetchCtx, u)
			if ok {
				select {
				case results <- fetchResult{body: body, url: u}:
				default:
				}
			}
		}(u)
	}

	var got *fetchResult
	for i := 0; i < len(candidates) && got == nil; i++ {
		select {
		case rr := <-results:
			got = &rr
		case <-fetchCtx.Done():
			got = nil
			i = len(candidates)
		}
	}

	if got != nil {
		body := got.body
		etag := imageETag(got.url)
		cachePath := filepath.Join(h.cacheDir, etag)
		tmpPath := fmt.Sprintf("%s.tmp.%d", cachePath, time.Now().UnixNano())
		if err := os.WriteFile(tmpPath, body, 0644); err == nil {
			_ = os.Rename(tmpPath, cachePath)
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Header().Set("Content-Type", detectContentType(got.url, body))
		_, _ = w.Write(body)
		return
	}

	// Every candidate failed: return a short-lived placeholder so the client
	// retries soon instead of caching a failure for a whole day.
	writeImagePlaceholder(w, r)
}

// writeImagePlaceholder serves a transparent 1x1 GIF with a short cache TTL.
// A stable ETag is still returned so clients can conditionally revalidate.
func writeImagePlaceholder(w http.ResponseWriter, r *http.Request) {
	const placeholderETag = `"placeholder"`
	if r != nil && r.Header.Get("If-None-Match") == placeholderETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", placeholderETag)
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "public, max-age=60")
	_, _ = w.Write(transparentGIF)
}

func imageETag(u string) string {
	h := sha256.New()
	h.Write([]byte(u))
	return hex.EncodeToString(h.Sum(nil))
}

// fetchImage downloads an image, following redirects, with a Referer header.
func (h *ImageHandlers) fetchImage(ctx context.Context, u string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/120 Safari/537.36")
	// Several image CDNs (e.g. AVDB's tu.djhdhs.us) reject requests without a Referer.
	if pu, perr := url.Parse(u); perr == nil {
		req.Header.Set("Referer", pu.Scheme+"://"+pu.Host+"/")
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil || len(body) < 100 {
		return nil, false
	}
	return body, true
}

func detectContentType(pathOrURL string, content []byte) string {
	lower := strings.ToLower(pathOrURL)
	if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
		return "image/jpeg"
	}
	if strings.HasSuffix(lower, ".png") {
		return "image/png"
	}
	if strings.HasSuffix(lower, ".webp") {
		return "image/webp"
	}
	return http.DetectContentType(content)
}

// parsePreviewImages parses preview_images, which may be a JSON array or a
// comma-separated list of URLs (as stored by the legacy offline database).
func parsePreviewImages(raw *string) []string {
	if raw == nil {
		return nil
	}
	s := strings.TrimSpace(*raw)
	if s == "" || s == "[]" {
		return nil
	}
	if strings.HasPrefix(s, "[") {
		var arr []string
		if json.Unmarshal([]byte(s), &arr) == nil && len(arr) > 0 {
			return arr
		}
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "http") {
			out = append(out, p)
		}
	}
	return out
}
