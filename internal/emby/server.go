package emby

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"

	"mediavault/internal/db"
	"mediavault/internal/services"

	"github.com/google/uuid"
)

type Server struct {
	serverID     string
	baseURL      string
	database     *db.DB
	movieRepo    *db.MovieRepo
	magnetRepo   *db.MagnetRepo
	assetRepo    *db.AssetRepo
	userRepo     *db.UserRepo
	progressRepo *db.ProgressRepo
	libraryRepo  *db.LibraryRepo
	resolver     *services.Resolver
	authHandlers *AuthHandlers
	views        *ViewsHandlers
	items        *ItemsHandlers
	playback     *PlaybackHandlers
	images       *ImageHandlers
	logger       *slog.Logger
}

func NewServer(
	database *db.DB,
	movieRepo *db.MovieRepo,
	magnetRepo *db.MagnetRepo,
	assetRepo *db.AssetRepo,
	userRepo *db.UserRepo,
	progressRepo *db.ProgressRepo,
	libraryRepo *db.LibraryRepo,
	resolver *services.Resolver,
	cacheDir string,
	baseURL string,
	logger *slog.Logger,
) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8096"
	}

	// Retrieve or generate stable ServerId from schema_meta
	serverID, err := getOrGenerateServerID(database)
	if err != nil {
		return nil, err
	}

	authHandlers := NewAuthHandlers(serverID, baseURL, userRepo)
	viewsHandlers := NewViewsHandlers(serverID, libraryRepo)
	itemsHandlers := NewItemsHandlers(serverID, database, movieRepo, magnetRepo, assetRepo, progressRepo)
	playbackHandlers := NewPlaybackHandlers(serverID, database, movieRepo, magnetRepo, assetRepo, progressRepo, resolver)
	imageHandlers := NewImageHandlers(cacheDir, movieRepo)

	return &Server{
		serverID:     serverID,
		baseURL:      baseURL,
		database:     database,
		movieRepo:    movieRepo,
		magnetRepo:   magnetRepo,
		assetRepo:    assetRepo,
		userRepo:     userRepo,
		progressRepo: progressRepo,
		libraryRepo:  libraryRepo,
		resolver:     resolver,
		authHandlers: authHandlers,
		views:        viewsHandlers,
		items:        itemsHandlers,
		playback:     playbackHandlers,
		images:       imageHandlers,
		logger:       logger,
	}, nil
}

func (s *Server) ServerID() string {
	return s.serverID
}

// ServeHTTP routes Emby requests with prefix stripping, case normalization, and auth.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	normPath, prefix := NormalizeEmbyPath(r.URL.Path)
	ctx := context.WithValue(r.Context(), prefixContextKey, prefix)
	r = r.WithContext(ctx)

	method := r.Method
	parts := strings.Split(strings.Trim(normPath, "/"), "/")

	// Public routes
	if normPath == "/system/info/public" && method == "GET" {
		s.authHandlers.GetPublicSystemInfo(w, r)
		return
	}
	if normPath == "/system/endpoint" && method == "GET" {
		s.authHandlers.GetEndpoint(w, r)
		return
	}
	if normPath == "/users/authenticatebyname" && method == "POST" {
		s.authHandlers.AuthenticateByName(w, r)
		return
	}

	// All other routes require Emby auth
	AuthMiddleware(s.userRepo, func(w http.ResponseWriter, r *http.Request) {
		s.dispatchProtected(w, r, method, normPath, parts)
	})(w, r)
}

func (s *Server) dispatchProtected(w http.ResponseWriter, r *http.Request, method, normPath string, parts []string) {
	// System info
	if normPath == "/system/info" && method == "GET" {
		s.authHandlers.GetSystemInfo(w, r)
		return
	}

	// Current user
	if normPath == "/users/me" && method == "GET" {
		s.authHandlers.GetMe(w, r)
		return
	}

	// Sessions capabilities & logout
	if (normPath == "/sessions/capabilities" || normPath == "/sessions/capabilities/full") && method == "POST" {
		s.authHandlers.PostCapabilities(w, r)
		return
	}
	if normPath == "/sessions/logout" && method == "POST" {
		s.authHandlers.PostLogout(w, r)
		return
	}

	// Sessions playing / progress / stopped
	if normPath == "/sessions/playing" && method == "POST" {
		s.playback.SessionsPlaying(w, r)
		return
	}
	if normPath == "/sessions/playing/progress" && method == "POST" {
		s.playback.SessionsProgress(w, r)
		return
	}
	if normPath == "/sessions/playing/stopped" && method == "POST" {
		s.playback.SessionsStopped(w, r)
		return
	}

	// Media folders & views
	if normPath == "/library/mediafolders" && method == "GET" {
		s.views.GetViews(w, r)
		return
	}

	// Counts & Nextup & Resume & Latest
	if normPath == "/items/counts" && method == "GET" {
		s.items.GetCounts(w, r)
		return
	}
	if normPath == "/shows/nextup" && method == "GET" {
		s.items.GetNextUp(w, r)
		return
	}
	if normPath == "/items/resume" && method == "GET" {
		s.items.GetResume(w, r)
		return
	}
	if normPath == "/items/latest" && method == "GET" {
		s.items.GetLatest(w, r)
		return
	}

	// /items (query collection)
	if normPath == "/items" && method == "GET" {
		s.items.GetItems(w, r)
		return
	}

	// Routing for /items/{id}/...
	if len(parts) >= 2 && parts[0] == "items" {
		itemID := parts[1]
		if len(parts) == 2 && method == "GET" {
			s.items.GetItemDetail(w, r, itemID)
			return
		}
		if len(parts) == 3 && parts[2] == "playbackinfo" && (method == "GET" || method == "POST") {
			s.playback.GetPlaybackInfo(w, r, itemID)
			return
		}
		if len(parts) >= 4 && parts[2] == "images" && method == "GET" {
			imageType := parts[3]
			s.images.ServeItemImage(w, r, itemID, imageType)
			return
		}
	}

	// Routing for /videos/{id}/stream
	if len(parts) >= 3 && parts[0] == "videos" {
		itemID := parts[1]
		streamSeg := parts[2]
		if strings.HasPrefix(streamSeg, "stream") && (method == "GET" || method == "HEAD") {
			s.playback.StreamHandler(w, r, itemID)
			return
		}
	}

	// Routing for /users/{uid}/...
	if len(parts) >= 2 && parts[0] == "users" {
		uid := parts[1]
		if len(parts) == 2 && method == "GET" {
			s.authHandlers.GetUserByID(w, r, uid)
			return
		}
		if len(parts) == 3 && parts[2] == "views" && method == "GET" {
			s.views.GetViews(w, r)
			return
		}
		if len(parts) == 3 && parts[2] == "items" && method == "GET" {
			s.items.GetItems(w, r)
			return
		}
		if len(parts) == 4 && parts[2] == "items" && parts[3] == "resume" && method == "GET" {
			s.items.GetResume(w, r)
			return
		}
		if len(parts) == 4 && parts[2] == "items" && parts[3] == "latest" && method == "GET" {
			s.items.GetLatest(w, r)
			return
		}
		if len(parts) == 4 && parts[2] == "items" && method == "GET" {
			itemID := parts[3]
			s.items.GetItemDetail(w, r, itemID)
			return
		}
		if len(parts) == 4 && parts[2] == "favoriteitems" {
			itemID := parts[3]
			if method == "POST" {
				s.items.FavoriteItem(w, r, itemID)
				return
			}
			if method == "DELETE" {
				s.items.UnfavoriteItem(w, r, itemID)
				return
			}
		}
		if len(parts) == 4 && parts[2] == "playeditems" {
			itemID := parts[3]
			if method == "POST" {
				s.items.MarkPlayedItem(w, r, itemID)
				return
			}
			if method == "DELETE" {
				s.items.UnmarkPlayedItem(w, r, itemID)
				return
			}
		}
	}

	http.NotFound(w, r)
}

func getOrGenerateServerID(database *db.DB) (string, error) {
	ctx := context.Background()
	var serverID string

	err := database.ExecRead(ctx, func(d *sql.DB) error {
		row := d.QueryRowContext(ctx, `SELECT value FROM schema_meta WHERE key = 'server_id'`)
		return row.Scan(&serverID)
	})

	if err == nil && serverID != "" {
		return serverID, nil
	}

	// Generate new serverID
	serverID = strings.ReplaceAll(uuid.New().String(), "-", "")
	err = database.ExecWrite(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO schema_meta (key, value) VALUES ('server_id', ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value;
		`, serverID)
		return err
	})
	if err != nil {
		return "", err
	}

	return serverID, nil
}
