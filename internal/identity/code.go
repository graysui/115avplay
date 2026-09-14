package identity

import (
	"regexp"
	"strings"
)

var (
	// Token delimiters: whitespace, underscore, brackets, slashes, dots, commas
	splitRegex = regexp.MustCompile(`[\s_\[\]\(\)\{\}\/\\,\.]+`)

	fc2Exact     = regexp.MustCompile(`(?i)^FC2(?:-?PPV)?-?(\d{5,8})$`)
	caribExact   = regexp.MustCompile(`(?i)^(?:carib(?:bean)?-?)?(\d{6}-\d{3})$`)
	luxuExact    = regexp.MustCompile(`(?i)^(\d{3}[A-Z]{3,5})-?(\d{3,5})$`)
	stdExact     = regexp.MustCompile(`(?i)^([A-Z]{2,6})-?(\d{2,5})$`)
	longNumExact = regexp.MustCompile(`(?i)^[A-Z]{2,6}\d{6,}$`)
)

// NormalizeCode extracts and normalizes the standard AV code from input text.
// Returns code and reason ("none", "ambiguous", "unsupported_number_length", or "").
func NormalizeCode(input string) (string, string) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", "none"
	}

	tokens := splitRegex.Split(trimmed, -1)

	// Check if any token has unsupported number length (e.g. IPX123456789)
	for _, t := range tokens {
		tUpper := strings.ToUpper(t)
		if !strings.HasPrefix(tUpper, "FC2") && !strings.HasPrefix(tUpper, "CARIB") {
			if longNumExact.MatchString(t) {
				return "", "unsupported_number_length"
			}
		}
	}

	var candidates []string

	for _, token := range tokens {
		if token == "" {
			continue
		}

		// 1. FC2
		if m := fc2Exact.FindStringSubmatch(token); len(m) > 0 {
			candidates = append(candidates, "FC2-PPV-"+m[1])
			continue
		}

		// 2. Caribbean / 010124-001
		if m := caribExact.FindStringSubmatch(token); len(m) > 0 {
			candidates = append(candidates, m[1])
			continue
		}

		// 3. 259LUXU-1234
		if m := luxuExact.FindStringSubmatch(token); len(m) > 0 {
			candidates = append(candidates, strings.ToUpper(m[1])+"-"+m[2])
			continue
		}

		// 4. Standard AAA-123 or AAA123
		if m := stdExact.FindStringSubmatch(token); len(m) > 0 {
			prefix := strings.ToUpper(m[1])
			num := m[2]
			if prefix == "H" || prefix == "FHD" || prefix == "UHD" || prefix == "MP" || prefix == "NO" {
				continue
			}
			candidates = append(candidates, prefix+"-"+num)
			continue
		}
	}

	// Deduplicate candidates
	unique := make(map[string]struct{})
	var deduplicated []string
	for _, c := range candidates {
		if _, exists := unique[c]; !exists {
			unique[c] = struct{}{}
			deduplicated = append(deduplicated, c)
		}
	}

	if len(deduplicated) == 0 {
		return "", "none"
	}
	if len(deduplicated) > 1 {
		return "", "ambiguous"
	}

	return deduplicated[0], ""
}
