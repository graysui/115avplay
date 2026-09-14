package logger

import (
	"regexp"
	"strings"
)

var sensitiveKeys = map[string]bool{
	"password":       true,
	"admin_password": true,
	"token":          true,
	"access_token":   true,
	"refresh_token":  true,
	"token_hash":     true,
	"secret":         true,
	"cookie":         true,
	"authorization":  true,
	"x-emby-token":   true,
	"api_key":        true,
	"webhook_url":    true,
}

var (
	// Regexes to scrub sensitive patterns from free-form strings
	jwtRegex    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9._-]*`)
	bearerRegex = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]+=*`)
	urlSigRegex = regexp.MustCompile(`(?i)(sign|signature|token|key|pwd)=[A-Za-z0-9._~%+-]+`)
)

// ScrubKey checks if an attribute key is sensitive.
func IsSensitiveKey(key string) bool {
	return sensitiveKeys[strings.ToLower(strings.TrimSpace(key))]
}

// ScrubValue sanitizes a string value.
func ScrubString(val string) string {
	s := jwtRegex.ReplaceAllString(val, "[REDACTED_JWT]")
	s = bearerRegex.ReplaceAllString(s, "Bearer [REDACTED]")
	s = urlSigRegex.ReplaceAllString(s, "$1=[REDACTED]")
	return s
}
