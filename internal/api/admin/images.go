package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mediavault/internal/api"
	"mediavault/internal/config"
	"mediavault/internal/db"
	"mediavault/internal/javdb"

	"github.com/gin-gonic/gin"
)

const maxProxiedImageBytes = 15 * 1024 * 1024

// ImageProxyHandler proxies and caches remote cover/poster images. JavDB's CDN
// serves images as application/octet-stream and may be unreachable from the
// browser directly; routing through the server makes the admin UI reliable.
type ImageProxyHandler struct {
	cacheDir   string
	httpClient *http.Client
	movieRepo  *db.MovieRepo
}

func NewImageProxyHandler(cfg *config.AppConfig, movieRepo *db.MovieRepo) *ImageProxyHandler {
	cacheDir := filepath.Join(cfg.DataDir, "cache", "admin_images")
	_ = os.MkdirAll(cacheDir, 0755)
	return &ImageProxyHandler{
		cacheDir:   cacheDir,
		movieRepo:  movieRepo,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// Proxy handles GET /api/v1/images/proxy?url=...
func (h *ImageProxyHandler) Proxy(c *gin.Context) {
	target := c.Query("url")
	if target == "" {
		api.SendError(c, http.StatusBadRequest, "invalid_params", "url is required")
		return
	}
	// Rewrite encrypted app-CDN URLs (tp.spfcas.com) to the public web CDN.
	target = javdb.NormalizeImageURL(target)

	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		api.SendError(c, http.StatusBadRequest, "invalid_url", "only http/https URLs are allowed")
		return
	}
	if !isSafeProxyHost(parsed.Hostname()) {
		api.SendError(c, http.StatusBadRequest, "blocked_host", "refusing to proxy private or local addresses")
		return
	}

	hasher := sha256.Sum256([]byte(target))
	etag := hex.EncodeToString(hasher[:])
	cachePath := filepath.Join(h.cacheDir, etag)

	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}

	// Serve from disk cache when available.
	if fi, err := os.Stat(cachePath); err == nil && fi.Size() > 0 {
		data, err := os.ReadFile(cachePath)
		if err == nil {
			c.Header("ETag", etag)
			c.Header("Cache-Control", "public, max-age=604800")
			c.Data(http.StatusOK, detectImageType(data), data)
			return
		}
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), "GET", target, nil)
	if err != nil {
		api.SendError(c, http.StatusBadGateway, "fetch_failed", err.Error())
		return
	}
	req.Header.Set("User-Agent", "MediaVault/1.0")
	req.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host+"/")

	resp, err := h.httpClient.Do(req)
	if err != nil {
		api.SendError(c, http.StatusBadGateway, "fetch_failed", "failed to fetch image: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		api.SendError(c, http.StatusBadGateway, "fetch_failed", "upstream returned status "+resp.Status)
		return
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxProxiedImageBytes))
	if err != nil {
		api.SendError(c, http.StatusBadGateway, "read_failed", "failed to read image: "+err.Error())
		return
	}

	_ = os.WriteFile(cachePath, data, 0644)

	c.Header("ETag", etag)
	c.Header("Cache-Control", "public, max-age=604800")
	c.Data(http.StatusOK, detectImageType(data), data)
}

// isSafeProxyHost rejects loopback/private/link-local hosts to mitigate SSRF.
func isSafeProxyHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return false
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		// If DNS fails, let the HTTP client surface the error instead of blocking.
		return true
	}
	for _, ip := range ips {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return false
		}
	}
	return true
}

func detectImageType(data []byte) string {
	if len(data) == 0 {
		return "application/octet-stream"
	}
	ct := http.DetectContentType(data)
	if strings.HasPrefix(ct, "image/") {
		return ct
	}
	// JavDB CDN often returns octet-stream; sniff common signatures.
	switch {
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8:
		return "image/jpeg"
	case len(data) > 8 && string(data[0:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	case strings.HasPrefix(string(data), "GIF8"):
		return "image/gif"
	}
	return "application/octet-stream"
}
