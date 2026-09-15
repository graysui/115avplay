package identity

import (
	"regexp"
	"strings"
)

var (
	// Non-anchored patterns with boundary checks, per
	// references/115_tree_scan_and_cleanup_spec.md. Non-anchored so that common
	// suffixes (-C 中文字幕, -U 无码, -UC, -CH, etc.) do not break the match.
	fc2Re   = regexp.MustCompile(`(?i)FC2(?:[-_ ]?PPV)?[-_ ]?([0-9]{5,8})`)
	datedRe = regexp.MustCompile(`(?i)(?:CARIB[-_ ]?)?([0-9]{6})[-_]([0-9]{3})`)
	stdRe   = regexp.MustCompile(`(?i)([0-9]{1,3}[A-Z]{2,10}|[A-Z]{2,10})[-_ ]?([0-9]{2,8})`)
	longNum = regexp.MustCompile(`(?i)[A-Z]{2,6}[0-9]{6,}`)
	// Subtitle/censorship suffixes glued to the number (856ch, 598c, 636uc, 101u).
	suffixRe = regexp.MustCompile(`(?i)([0-9])-?(chs|ch|uncensored|uc|c|u)([^a-z0-9]|$)`)
	// Spam domain tags such as kckc13.com@SDJS-156.
	domainRe = regexp.MustCompile(`(?i)[a-z0-9][a-z0-9-]*\.(?:com|net|org|app|cc|vip|top|xyz|info|me|us|tv|pw|site|club|live|online)`)
)

// Noise prefixes that look like codes but are quality/forum tags.
var codeNoisePrefix = map[string]bool{
	"HD": true, "FHD": true, "UHD": true, "HEVC": true, "AVC": true,
	"H264": true, "H265": true, "SIS": true, "SIS001": true, "MGS": true,
}

type codeSpan struct{ start, end int }

func asciiAlnum(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

func codeBoundary(s string, a, b int) bool {
	return (a == 0 || !asciiAlnum(s[a-1])) && (b == len(s) || !asciiAlnum(s[b]))
}

// NormalizeCode extracts and normalizes the standard AV code from input text.
// Returns code and reason ("none", "ambiguous", or "").
func NormalizeCode(input string) (string, string) {
	name := strings.TrimSpace(input)
	if name == "" {
		return "", "none"
	}
	// Remove spam domain tags so they don't create false candidates.
	name = domainRe.ReplaceAllString(name, " ")
	// Remove trailing subtitle/censorship markers so the number keeps a boundary.
	name = suffixRe.ReplaceAllString(name, "$1$3")

	found := map[string]bool{}
	var reserved []codeSpan
	add := func(m []int, code string) {
		if codeBoundary(name, m[0], m[1]) {
			found[code] = true
			reserved = append(reserved, codeSpan{m[0], m[1]})
		}
	}

	// 1. FC2 (FC2PPV-1234567 / FC2-PPV-1234567 / FC2-1234567)
	for _, m := range fc2Re.FindAllStringSubmatchIndex(name, -1) {
		add(m, "FC2-PPV-"+name[m[2]:m[3]])
	}

	// 2. Caribbean dated codes (010124-001)
	for _, m := range datedRe.FindAllStringSubmatchIndex(name, -1) {
		add(m, name[m[2]:m[3]]+"-"+name[m[4]:m[5]])
	}

	// 3. Standard prefix-number (incl. 259LUXU-1234), skipping overlapping spans
	//    and known noise tags.
	for _, m := range stdRe.FindAllStringSubmatchIndex(name, -1) {
		overlap := false
		for _, r := range reserved {
			if m[0] < r.end && m[1] > r.start {
				overlap = true
				break
			}
		}
		prefix := strings.ToUpper(name[m[2]:m[3]])
		num := name[m[4]:m[5]]
		if !overlap && !codeNoisePrefix[prefix] && codeBoundary(name, m[0], m[1]) {
			found[prefix+"-"+num] = true
		}
	}

	if len(found) == 0 {
		if m := longNum.FindStringIndex(name); m != nil && codeBoundary(name, m[0], m[1]) {
			return "", "unsupported_number_length"
		}
		return "", "none"
	}
	if len(found) > 1 {
		return "", "ambiguous"
	}
	for code := range found {
		return code, ""
	}
	return "", "none"
}
