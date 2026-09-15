package middleware

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// RequestLogger logs every HTTP request (admin API and Emby clients) as a single
// Chinese message containing method, path, status, latency and client address.
// Sensitive query parameters are redacted.
func RequestLogger(logger *slog.Logger) gin.HandlerFunc {
	if logger == nil {
		logger = slog.Default()
	}
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		latency := time.Since(start)

		status := c.Writer.Status()
		path := c.Request.URL.Path
		if p := c.Request.URL.RawQuery; p != "" {
			path += "?" + redactQuery(p)
		}

		msg := "HTTP请求 " + c.Request.Method + " " + path +
			" -> " + strconv.Itoa(status) +
			" 耗时=" + latency.String() +
			" 来源=" + c.ClientIP()
		if len(msg) > 800 {
			msg = msg[:800] + "...(截断)"
		}

		// Routine polling/health/image requests are demoted to DEBUG so they do
		// not flood the console. Errors (>=400) are always shown.
		if status < 400 && isQuietRequest(c.Request.URL.Path) {
			logger.Debug(msg)
			return
		}

		switch {
		case status >= 500:
			logger.Error(msg)
		case status >= 400:
			logger.Warn(msg)
		default:
			logger.Info(msg)
		}
	}
}

// isQuietRequest reports whether a request is routine noise (polling, health
// checks, cover images) that should only be logged at DEBUG level.
func isQuietRequest(path string) bool {
	switch path {
	case "/healthz", "/readyz":
		return true
	}
	for _, prefix := range []string{
		"/api/v1/scraper/status",
		"/api/v1/stats",
		"/api/v1/115/status",
		"/api/v1/javdb/status",
		"/api/v1/system/schema",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	// Emby cover/backdrop image requests are extremely frequent.
	if strings.Contains(strings.ToLower(path), "/images/") {
		return true
	}
	return false
}

// redactQuery masks token-like query parameters before logging.
func redactQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	for i, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.ToLower(kv[0]) {
		case "token", "api_key", "apikey", "x-emby-token", "access_token", "password":
			parts[i] = kv[0] + "=[已隐藏]"
		}
	}
	return strings.Join(parts, "&")
}
