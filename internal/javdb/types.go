package javdb

import (
	"strings"
)

type MovieDetailDTO struct {
	ID             string   `json:"id"`
	Number         string   `json:"number"`
	Title          string   `json:"title"`
	OfficialTitle  *string  `json:"official_title,omitempty"`
	TitleZH        *string  `json:"title_zh,omitempty"`
	DescriptionZH  *string  `json:"description_zh,omitempty"`
	CoverURL       *string  `json:"cover_url,omitempty"`
	PosterURL      *string  `json:"poster_url,omitempty"`
	Actors         []string `json:"actors"`
	Tags           []string `json:"tags"`
	Maker          *string  `json:"maker,omitempty"`
	Director       *string  `json:"director,omitempty"`
	Score          *float64 `json:"score,omitempty"`
	ReleaseDate    *string  `json:"release_date,omitempty"`
	RuntimeSeconds *int     `json:"runtime_seconds,omitempty"`
	VideoType      string   `json:"video_type"`
}

type MagnetDTO struct {
	Name      string `json:"name"`
	MagnetURL string `json:"magnet_url"`
	SizeBytes int64  `json:"size_bytes"`
	HasHD     bool   `json:"has_hd"`
	HasSub    bool   `json:"has_sub"`
	Seeders   int    `json:"seeders"`
}

type ReviewDTO struct {
	UserName string   `json:"user_name"`
	Score    *float64 `json:"score,omitempty"`
	Date     string   `json:"date"`
	Links    []string `json:"links"`
}

type RankingMovieDTO struct {
	ID          string   `json:"id"`
	Number      string   `json:"number"`
	Title       string   `json:"title"`
	CoverURL    *string  `json:"cover_url,omitempty"`
	Score       *float64 `json:"score,omitempty"`
	ReleaseDate *string  `json:"release_date,omitempty"`
	Rank        int      `json:"rank"`
	VideoType   string   `json:"video_type"`
}

// CalculateCompleteness determines is_enriched and whether all core fields are present.
// JavDB only provides the Japanese title, cover, score, actors and tags; it never
// provides Chinese titles/descriptions, so those must NOT be treated as required.
func CalculateCompleteness(d *MovieDetailDTO) (isEnriched int, isSuccess bool) {
	hasTitle := false
	if d.TitleZH != nil && strings.TrimSpace(*d.TitleZH) != "" {
		hasTitle = true
	} else if d.OfficialTitle != nil && strings.TrimSpace(*d.OfficialTitle) != "" {
		hasTitle = true
	} else if strings.TrimSpace(d.Title) != "" {
		hasTitle = true
	}

	hasCover := d.CoverURL != nil && strings.TrimSpace(*d.CoverURL) != ""
	hasDesc := d.DescriptionZH != nil && strings.TrimSpace(*d.DescriptionZH) != ""

	// Complete = title + cover (what the data source can provide).
	if hasTitle && hasCover {
		return 1, true
	}

	if hasTitle || hasCover || hasDesc || len(d.Actors) > 0 || len(d.Tags) > 0 || d.Score != nil {
		return 3, false // Partial
	}

	return 0, false // No enriched metadata
}
