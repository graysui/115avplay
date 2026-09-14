package ingestion

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"mediavault/internal/identity"
	"strconv"
	"strings"
)

const (
	CategoryExcluded = "EXCLUDED"
	CategoryExempt   = "EXEMPT"
)

// RawCSVRow represents an unparsed record from AVDB CSV.
type RawCSVRow struct {
	Number        string
	Magnet        string
	Title         string
	Section       string
	PublishDate   string
	PreviewImages string
	SizeStr       string
	SourceWebsite string
}

// IngestRecord represents a fully normalized and parsed record ready for DB.
type IngestRecord struct {
	Code          string
	Title         string
	Category      string
	PublishDate   *string
	PreviewImages []string
	SourceWebsite string
	ScrapePolicy  string
	PolicyReason  *string
	IsEnriched    int

	// Resource / Magnet properties
	ResourceKind  string
	ResourceKey   string
	Quality       identity.QualityFlags
	QualityLabel  string
	SizeBytes     int64
	Section       string
	MagnetURL     string
}

// NormalizeSection determines standard category and whether it should be excluded or exempt.
// Returns normalized category, isExcluded, isExempt.
func NormalizeSection(rawSection, rawTitle string) (category string, isExcluded bool, isExempt bool) {
	sec := strings.TrimSpace(rawSection)
	titleUpper := strings.ToUpper(rawTitle)

	// 1. Check strict exclusions
	switch sec {
	case "VR视频区", "VR", "VR 视频区", "欧美无码", "欧美", "三级写真", "写真":
		return CategoryExcluded, true, false
	}

	if strings.Contains(titleUpper, "VR") ||
		strings.Contains(rawTitle, "三级") ||
		strings.Contains(rawTitle, "写真") {
		return CategoryExcluded, true, false
	}

	// 2. Check exempt categories
	switch sec {
	case "FC2", "FC2/素人", "素人", "素人有码", "MGS":
		return "FC2/素人", false, true
	case "国产", "国内成人", "主播精选", "探花精选", "韩国主播":
		return "国产", false, true
	}

	// Title-based checks for exempt
	if strings.Contains(titleUpper, "FC2") || strings.Contains(titleUpper, "SIRO") {
		return "FC2/素人", false, true
	}

	// 3. Standard sections
	switch sec {
	case "中文字幕", "高清中文字幕":
		return "中文字幕", false, false
	case "4K原版", "4K":
		return "4K原版", false, false
	case "亚洲无码":
		return "亚洲无码", false, false
	case "亚洲有码", "动漫原创":
		return "亚洲有码", false, false
	}

	// 4. Fallback heuristics from title
	if strings.Contains(rawTitle, "中文字幕") ||
		strings.Contains(rawTitle, "中字") ||
		strings.Contains(titleUpper, "-C") ||
		strings.Contains(titleUpper, "_C") {
		return "中文字幕", false, false
	}
	if strings.Contains(titleUpper, "4K") || strings.Contains(titleUpper, "2160P") {
		return "4K原版", false, false
	}
	if strings.Contains(rawTitle, "无码") || strings.Contains(titleUpper, "UNCENSORED") {
		return "亚洲无码", false, false
	}

	return "亚洲有码", false, false
}

// ParseCSVRecords parses an input CSV byte stream and yields RawCSVRow records.
func ParseCSVStream(data []byte, sourceWebsite string, handleRow func(row RawCSVRow) error) error {
	// Trim UTF-8 BOM if present
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1 // flexible fields
	reader.LazyQuotes = true

	headers, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read csv headers: %w", err)
	}

	headerMap := make(map[string]int)
	for i, h := range headers {
		headerMap[strings.ToLower(strings.TrimSpace(h))] = i
	}

	numIdx, hasNum := headerMap["number"]
	magIdx, hasMag := headerMap["magnet"]
	if !hasNum || !hasMag {
		return fmt.Errorf("csv missing required header 'number' or 'magnet'")
	}

	titleIdx, hasTitle := headerMap["title"]
	secIdx, hasSec := headerMap["section"]
	pubIdx, hasPub := headerMap["publish_date"]
	prevIdx, hasPrev := headerMap["preview_images"]
	sizeIdx, hasSize := headerMap["size"]

	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Skip corrupted single row
			continue
		}

		if len(record) <= numIdx || len(record) <= magIdx {
			continue
		}

		raw := RawCSVRow{
			Number:        strings.TrimSpace(record[numIdx]),
			Magnet:        strings.TrimSpace(record[magIdx]),
			SourceWebsite: sourceWebsite,
		}

		if hasTitle && len(record) > titleIdx {
			raw.Title = strings.TrimSpace(record[titleIdx])
		}
		if hasSec && len(record) > secIdx {
			raw.Section = strings.TrimSpace(record[secIdx])
		}
		if hasPub && len(record) > pubIdx {
			raw.PublishDate = strings.TrimSpace(record[pubIdx])
		}
		if hasPrev && len(record) > prevIdx {
			raw.PreviewImages = strings.TrimSpace(record[prevIdx])
		}
		if hasSize && len(record) > sizeIdx {
			raw.SizeStr = strings.TrimSpace(record[sizeIdx])
		}

		if err := handleRow(raw); err != nil {
			return err
		}
	}

	return nil
}

// ParseRawRow converts RawCSVRow to IngestRecord using domain pure functions.
// Returns nil if row is excluded or invalid.
func ParseRawRow(raw RawCSVRow) (*IngestRecord, error) {
	if raw.Number == "" || raw.Magnet == "" {
		return nil, nil
	}

	cat, isExcluded, isExempt := NormalizeSection(raw.Section, raw.Title)
	if isExcluded {
		return nil, nil
	}

	code, reason := identity.NormalizeCode(raw.Number)
	if code == "" || reason == "unsupported_number_length" {
		return nil, nil
	}

	kind, key, err := identity.ParseResourceURI(raw.Magnet)
	if err != nil || kind == identity.KindUnsupported {
		return nil, nil
	}

	quality := identity.ExtractQualityFlags(raw.Title)
	qualityLabel := "1080P"
	if quality.Is4K == 1 {
		qualityLabel = "4K"
	}

	// Parse size
	var sizeBytes int64
	if raw.SizeStr != "" {
		if val, err := strconv.ParseFloat(raw.SizeStr, 64); err == nil && val > 0 {
			// If value is small, it's typically in MB (as in AVDB CSVs)
			if val < 1000000 {
				sizeBytes = int64(val * 1024 * 1024)
			} else {
				sizeBytes = int64(val)
			}
		}
	}

	// Preview images
	var previews []string
	if raw.PreviewImages != "" {
		for _, img := range strings.Split(raw.PreviewImages, ",") {
			img = strings.TrimSpace(img)
			if strings.HasPrefix(img, "http") {
				previews = append(previews, img)
			}
		}
	}

	var pubDate *string
	if raw.PublishDate != "" {
		p := raw.PublishDate
		pubDate = &p
	}

	scrapePolicy := "auto"
	var policyReason *string
	if isExempt {
		scrapePolicy = "exempt"
		reason := "category"
		policyReason = &reason
	}

	return &IngestRecord{
		Code:          code,
		Title:         raw.Title,
		Category:      cat,
		PublishDate:   pubDate,
		PreviewImages: previews,
		SourceWebsite: raw.SourceWebsite,
		ScrapePolicy:  scrapePolicy,
		PolicyReason:  policyReason,
		IsEnriched:    0,
		ResourceKind:  string(kind),
		ResourceKey:   key,
		Quality:       quality,
		QualityLabel:  qualityLabel,
		SizeBytes:     sizeBytes,
		Section:       raw.Section,
		MagnetURL:     raw.Magnet,
	}, nil
}
