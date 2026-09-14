package identity

import (
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// ResourceKind represents supported resource identity kinds: btih, ed2k, existing, or unsupported.
type ResourceKind string

const (
	KindBTIH        ResourceKind = "btih"
	KindED2K        ResourceKind = "ed2k"
	KindExisting    ResourceKind = "existing"
	KindUnsupported ResourceKind = "unsupported"
)

var (
	hex40Regex = regexp.MustCompile(`^[0-9A-Fa-f]{40}$`)
	md4Regex   = regexp.MustCompile(`^[0-9A-Fa-f]{32}$`)
	ed2kRegex  = regexp.MustCompile(`(?i)^ed2k://\|file\|([^|]+)\|(\d+)\|([0-9A-Fa-f]{32})\|/`)
)

// ParseResourceURI parses a resource URI (magnet, ed2k, or 115 existing) and returns kind and normalized resource key.
func ParseResourceURI(uri string) (ResourceKind, string, error) {
	trimmed := strings.TrimSpace(uri)
	if strings.HasPrefix(strings.ToLower(trimmed), "magnet:") {
		return parseMagnet(trimmed)
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "ed2k://") {
		return parseED2K(trimmed)
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "115:") {
		parts := strings.Split(trimmed, ":")
		if len(parts) == 3 && parts[1] != "" && parts[2] != "" {
			return KindExisting, trimmed, nil
		}
	}
	return KindUnsupported, "", fmt.Errorf("unsupported resource uri scheme")
}

func parseMagnet(magnetURI string) (ResourceKind, string, error) {
	u, err := url.Parse(magnetURI)
	if err != nil {
		return KindUnsupported, "", fmt.Errorf("invalid magnet url: %w", err)
	}

	// Check xt params
	q := u.Query()
	xts := q["xt"]
	for _, xt := range xts {
		xtLower := strings.ToLower(xt)
		if strings.HasPrefix(xtLower, "urn:btih:") {
			rawHash := strings.TrimPrefix(xt, xt[:len("urn:btih:")])
			cleanHash := strings.TrimSpace(rawHash)

			// 1. Check 40-char Hex
			if len(cleanHash) == 40 && hex40Regex.MatchString(cleanHash) {
				return KindBTIH, strings.ToUpper(cleanHash), nil
			}

			// 2. Check 32-char Base32
			if len(cleanHash) == 32 {
				// Base32 decoding (RFC 4648 standard)
				decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(cleanHash))
				if err == nil && len(decoded) == 20 {
					return KindBTIH, strings.ToUpper(hex.EncodeToString(decoded)), nil
				}
			}
			return KindUnsupported, "", fmt.Errorf("invalid btih hash format")
		}
		if strings.HasPrefix(xtLower, "urn:btmh:") {
			// BT v2 btmh is unsupported in this version
			return KindUnsupported, "", nil
		}
	}

	return KindUnsupported, "", fmt.Errorf("missing urn:btih parameter")
}

func parseED2K(ed2kURI string) (ResourceKind, string, error) {
	m := ed2kRegex.FindStringSubmatch(ed2kURI)
	if len(m) != 4 {
		return KindUnsupported, "", fmt.Errorf("invalid ed2k url format")
	}

	sizeStr := m[2]
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil || size < 0 {
		return KindUnsupported, "", fmt.Errorf("invalid ed2k file size")
	}

	md4Hex := strings.ToUpper(m[3])
	key := fmt.Sprintf("ED2K:%s:%d", md4Hex, size)
	return KindED2K, key, nil
}

// FormatExistingKey formats an existing 115 file identity.
func FormatExistingKey(bindingID, fileID string) string {
	return fmt.Sprintf("115:%s:%s", bindingID, fileID)
}
