package emby

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"mediavault/internal/db"
	"mediavault/internal/models"
)

type contextKey string

const (
	userContextKey    contextKey = "emby_user"
	sessionContextKey contextKey = "emby_session"
	prefixContextKey  contextKey = "emby_prefix"
)

// UserFromContext retrieves the authenticated user from request context.
func UserFromContext(ctx context.Context) *models.User {
	if u, ok := ctx.Value(userContextKey).(*models.User); ok {
		return u
	}
	return nil
}

// SessionFromContext retrieves the authenticated session from request context.
func SessionFromContext(ctx context.Context) *models.AuthSession {
	if s, ok := ctx.Value(sessionContextKey).(*models.AuthSession); ok {
		return s
	}
	return nil
}

// PrefixFromContext retrieves the route prefix ("/emby" or "") from request context.
func PrefixFromContext(ctx context.Context) string {
	if p, ok := ctx.Value(prefixContextKey).(string); ok {
		return p
	}
	return ""
}

// EmbyAuthHeader contains client/device metadata from X-Emby-Authorization.
type EmbyAuthHeader struct {
	Client   string
	Device   string
	DeviceId string
	Version  string
	Token    string
	UserId   string
}

// ParseEmbyAuthHeader parses X-Emby-Authorization or Authorization header value.
// E.g.: MediaBrowser Client="Infuse", Device="Apple TV", DeviceId="ABC", Version="7.5", Token="xyz"
func ParseEmbyAuthHeader(authHeader string) EmbyAuthHeader {
	var res EmbyAuthHeader
	authHeader = strings.TrimSpace(authHeader)
	if strings.HasPrefix(strings.ToLower(authHeader), "mediabrowser ") {
		authHeader = strings.TrimSpace(authHeader[len("mediabrowser "):])
	}

	parts := strings.Split(authHeader, ",")
	for _, part := range parts {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(kv[0]))
		val := strings.Trim(strings.TrimSpace(kv[1]), "\"")

		switch key {
		case "client":
			res.Client = val
		case "device":
			res.Device = val
		case "deviceid":
			res.DeviceId = val
		case "version":
			res.Version = val
		case "token":
			res.Token = val
		case "userid":
			res.UserId = val
		}
	}
	return res
}

// GetQueryParam retrieves a query parameter by key case-insensitively.
func GetQueryParam(r *http.Request, key string) string {
	target := strings.ToLower(key)
	for k, vals := range r.URL.Query() {
		if strings.ToLower(k) == target && len(vals) > 0 {
			return vals[0]
		}
	}
	return ""
}

// ExtractEmbyToken extracts token from X-Emby-Token, api_key query param, or Authorization header.
// If multiple non-empty tokens exist and differ, returns ErrTokenConflict.
var ErrTokenConflict = errors.New("conflicting authentication tokens provided")

func ExtractEmbyToken(r *http.Request) (string, error) {
	var tokens []string

	// 1. X-Emby-Token header
	if t := strings.TrimSpace(r.Header.Get("X-Emby-Token")); t != "" {
		tokens = append(tokens, t)
	}

	// 2. api_key or ApiKey query param
	if t := strings.TrimSpace(GetQueryParam(r, "api_key")); t != "" {
		tokens = append(tokens, t)
	}

	// 3. X-Emby-Authorization header
	if auth := r.Header.Get("X-Emby-Authorization"); auth != "" {
		h := ParseEmbyAuthHeader(auth)
		if h.Token != "" {
			tokens = append(tokens, h.Token)
		}
	}

	// 4. Authorization header
	if auth := r.Header.Get("Authorization"); auth != "" {
		h := ParseEmbyAuthHeader(auth)
		if h.Token != "" {
			tokens = append(tokens, h.Token)
		}
	}

	if len(tokens) == 0 {
		return "", nil
	}

	first := tokens[0]
	for _, t := range tokens[1:] {
		if t != first {
			return "", ErrTokenConflict
		}
	}

	return first, nil
}

// NormalizeEmbyPath strips optional /emby prefix and normalizes static path segments to lowercase,
// preserving dynamic IDs (mov_*, src_*, usr_*, etc.) and returning the stripped prefix ("/emby" or "").
func NormalizeEmbyPath(rawPath string) (normalizedPath string, prefix string) {
	p := rawPath
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}

	cleanPrefix := ""
	if strings.HasPrefix(strings.ToLower(p), "/emby/") {
		cleanPrefix = "/emby"
		p = p[5:] // Keep the leading slash of the remainder
	} else if strings.EqualFold(p, "/emby") {
		cleanPrefix = "/emby"
		p = "/"
	}

	segments := strings.Split(p, "/")
	var normSegments []string
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		// Preserve case for IDs and tokens: mov_*, src_*, usr_*
		lower := strings.ToLower(seg)
		if strings.HasPrefix(seg, "mov_") || strings.HasPrefix(seg, "src_") || strings.HasPrefix(seg, "usr_") {
			normSegments = append(normSegments, seg)
		} else {
			normSegments = append(normSegments, lower)
		}
	}

	normalizedPath = "/" + strings.Join(normSegments, "/")
	return normalizedPath, cleanPrefix
}

// AuthMiddleware enforces audience='emby' authentication on protected endpoints.
func AuthMiddleware(userRepo *db.UserRepo, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := ExtractEmbyToken(r)
		if err != nil {
			if errors.Is(err, ErrTokenConflict) {
				http.Error(w, `{"error":"token conflict"}`, http.StatusUnauthorized)
				return
			}
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		if token == "" {
			http.Error(w, `{"error":"missing authentication token"}`, http.StatusUnauthorized)
			return
		}

		// Hash token with SHA256
		hasher := sha256.New()
		hasher.Write([]byte(token))
		tokenHash := hex.EncodeToString(hasher.Sum(nil))

		now := models.UTCNow()
		session, user, err := userRepo.GetSessionByTokenHash(r.Context(), tokenHash, "emby", now)
		if err != nil || user == nil || user.Enabled != 1 {
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userContextKey, user)
		ctx = context.WithValue(ctx, sessionContextKey, session)
		next(w, r.WithContext(ctx))
	}
}
