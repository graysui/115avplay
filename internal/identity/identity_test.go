package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type TestFixture struct {
	Normalization []struct {
		Input  string `json:"input"`
		Code   string `json:"code"`
		Reason string `json:"reason,omitempty"`
	} `json:"normalization"`
	Resources []struct {
		URI  string `json:"uri"`
		Kind string `json:"kind"`
		Key  *string `json:"key"`
	} `json:"resources"`
	Quality []struct {
		Title string `json:"title"`
		Flags []int  `json:"flags"`
		Score int    `json:"score"`
	} `json:"quality"`
}

func TestResourceIdentityExamples(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "references", "resource_identity_examples.json")
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", fixturePath, err)
	}

	var fixture TestFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("failed to unmarshal fixture: %v", err)
	}

	// 1. Test Code Normalization
	for _, tc := range fixture.Normalization {
		code, reason := NormalizeCode(tc.Input)
		if code != tc.Code {
			t.Errorf("NormalizeCode(%q): expected code %q, got %q (reason=%s)", tc.Input, tc.Code, code, reason)
		}
		if tc.Reason != "" && reason != tc.Reason {
			t.Errorf("NormalizeCode(%q): expected reason %q, got %q", tc.Input, tc.Reason, reason)
		}
	}

	// 2. Test Resource URIs
	for _, tc := range fixture.Resources {
		kind, key, err := ParseResourceURI(tc.URI)
		if string(kind) != tc.Kind {
			t.Errorf("ParseResourceURI(%q): expected kind %s, got %s (err=%v)", tc.URI, tc.Kind, kind, err)
		}
		expectedKey := ""
		if tc.Key != nil {
			expectedKey = *tc.Key
		}
		if key != expectedKey {
			t.Errorf("ParseResourceURI(%q): expected key %q, got %q", tc.URI, expectedKey, key)
		}
	}

	// 3. Test Quality Scoring
	for _, tc := range fixture.Quality {
		flags := ExtractQualityFlags(tc.Title)
		if flags.Score != tc.Score {
			t.Errorf("ExtractQualityFlags(%q): expected score %d, got %d (flags=%+v)", tc.Title, tc.Score, flags.Score, flags)
		}
		if flags.HasChineseSub != tc.Flags[0] || flags.IsCracked != tc.Flags[1] ||
			flags.Is4K != tc.Flags[2] || flags.IsCensored != tc.Flags[3] {
			t.Errorf("ExtractQualityFlags(%q): expected flags %v, got [%d,%d,%d,%d]",
				tc.Title, tc.Flags, flags.HasChineseSub, flags.IsCracked, flags.Is4K, flags.IsCensored)
		}
	}
}
