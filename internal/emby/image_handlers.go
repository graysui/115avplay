package emby

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mediavault/internal/db"
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
			Timeout: 10 * time.Second,
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

	var targetURL string
	imageTypeLower := strings.ToLower(imageType)

	if imageTypeLower == "primary" || imageTypeLower == "thumb" {
		if movie.CoverURL != nil && *movie.CoverURL != "" {
			targetURL = *movie.CoverURL
		} else if movie.PosterURL != nil && *movie.PosterURL != "" {
			targetURL = *movie.PosterURL
		}
	} else if imageTypeLower == "backdrop" {
		if movie.PreviewImages != nil && *movie.PreviewImages != "" {
			var previews []string
			if err := json.Unmarshal([]byte(*movie.PreviewImages), &previews); err == nil && len(previews) > 0 {
				targetURL = previews[0]
			}
		}
		if targetURL == "" && movie.CoverURL != nil && *movie.CoverURL != "" {
			targetURL = *movie.CoverURL
		}
	}

	if targetURL == "" {
		placeholderETag := `"placeholder"`
		if r.Header.Get("If-None-Match") == placeholderETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", placeholderETag)
		w.Header().Set("Content-Type", "image/gif")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(transparentGIF)
		return
	}

	// Calculate ETag based on targetURL
	hasher := sha256.New()
	hasher.Write([]byte(targetURL))
	etag := hex.EncodeToString(hasher.Sum(nil))

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	cachePath := filepath.Join(h.cacheDir, etag)
	// Check if already in disk cache
	if fileInfo, err := os.Stat(cachePath); err == nil && fileInfo.Size() > 0 {
		content, err := os.ReadFile(cachePath)
		if err == nil {
			w.Header().Set("ETag", etag)
			w.Header().Set("Cache-Control", "public, max-age=604800")
			w.Header().Set("Content-Type", detectContentType(cachePath, content))
			_, _ = w.Write(content)
			return
		}
	}

	// Fetch from upstream URL
	req, err := http.NewRequestWithContext(r.Context(), "GET", targetURL, nil)
	if err != nil {
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write(transparentGIF)
		return
	}
	req.Header.Set("User-Agent", "MediaVault/1.0")

	resp, err := h.httpClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write(transparentGIF)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write(transparentGIF)
		return
	}

	// Save to disk cache atomically
	tmpPath := fmt.Sprintf("%s.tmp.%d", cachePath, time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, body, 0644); err == nil {
		_ = os.Rename(tmpPath, cachePath)
	}

	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "public, max-age=604800")
	w.Header().Set("Content-Type", detectContentType(targetURL, body))
	_, _ = w.Write(body)
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
