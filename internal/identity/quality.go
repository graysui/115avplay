package identity

import (
	"regexp"
)

// QualityFlags holds extracted resource quality attributes.
type QualityFlags struct {
	HasChineseSub int // 0 or 1
	IsCracked     int // 0 or 1
	Is4K          int // 0 or 1
	IsCensored    int // 0 or 1
	Score         int // 8*has_chinese_sub + 4*is_cracked + 2*is_4k + is_censored
}

var (
	// Negative rules
	noSubRegex        = regexp.MustCompile(`(?i)(无字幕|无中字|无中文字幕|NO\s+SUB|NO\s+CHINESE\s+SUB)`)
	uncensoredRegex   = regexp.MustCompile(`(?i)(UNCENSORED|无码|無碼)`)

	// Positive Chinese Sub rules
	chineseSubRegex   = regexp.MustCompile(`(?i)(中文字幕|中字软字幕|中字|[-_](?:C|CH)\b)`)

	// Cracked rules
	crackedRegex      = regexp.MustCompile(`(?i)(破解|无码流出|無碼流出|LEAKED)`)

	// 4K rules
	fourKRegex        = regexp.MustCompile(`(?i)\b(4K|2160P|UHD)\b`)

	// Censored rules
	censoredRegex     = regexp.MustCompile(`(?i)(有码|有碼|CENSORED)`)
)

// ExtractQualityFlags extracts quality flags and computes the priority score from title according to design §7.2.
func ExtractQualityFlags(title string) QualityFlags {
	flags := QualityFlags{}

	// 1. Chinese Subtitles: Check negative words first
	if !noSubRegex.MatchString(title) {
		if chineseSubRegex.MatchString(title) {
			flags.HasChineseSub = 1
		}
	}

	// 2. Cracked (Leaked)
	if crackedRegex.MatchString(title) {
		flags.IsCracked = 1
	}

	// 3. 4K / UHD / 2160P
	if fourKRegex.MatchString(title) {
		flags.Is4K = 1
	}

	// 4. Censored: UNCENSORED / 无码 explicitly negates censored
	if !uncensoredRegex.MatchString(title) {
		if censoredRegex.MatchString(title) {
			flags.IsCensored = 1
		}
	}

	// Calculate score
	flags.Score = ComputePriorityScore(flags.HasChineseSub, flags.IsCracked, flags.Is4K, flags.IsCensored)
	return flags
}

// ComputePriorityScore calculates the bit-weighted priority score (0 ~ 15).
func ComputePriorityScore(hasChineseSub, isCracked, is4K, isCensored int) int {
	return 8*hasChineseSub + 4*isCracked + 2*is4K + isCensored
}
