package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed all:dist
var distFS embed.FS

// DistFileSystem returns an http.FileSystem rooted at dist subfolder.
func DistFileSystem() (http.FileSystem, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	return http.FS(sub), nil
}

// RegisterWebRoutes sets up static file serving and SPA fallback to index.html.
func RegisterWebRoutes(r *gin.Engine) error {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return err
	}
	httpFS := http.FS(sub)

	fileServer := http.FileServer(httpFS)

	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		// Never handle /api/ or /emby/ routes with SPA fallback
		if strings.HasPrefix(path, "/api/") || strings.HasPrefix(path, "/emby/") ||
			strings.HasPrefix(path, "/healthz") || strings.HasPrefix(path, "/readyz") {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"code":    "not_found",
					"message": "route not found",
				},
			})
			return
		}

		// Try serving static file directly
		f, err := sub.Open(strings.TrimPrefix(path, "/"))
		if err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}

		// SPA fallback to index.html for client-side routing
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})

	return nil
}
