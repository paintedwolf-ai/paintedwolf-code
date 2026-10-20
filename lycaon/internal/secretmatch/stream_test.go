package secretmatch

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/regexp"
)

func TestStreamCatalogMatchesWholePolicy(t *testing.T) {
	value := "fixture_123456abcdef"
	rule := config.Rule{RuleID: "fixture", Description: "Synthetic fixture", Regex: regexp.MustCompile(`(?i)fixture_[a-z0-9]{12}`), Keywords: []string{"fixture"}}
	detector := detect.NewDetector(config.Config{Rules: map[string]config.Rule{"fixture": rule}, Keywords: map[string]struct{}{"fixture": {}}})
	detector.MaxDecodeDepth = 2
	m := &Matcher{detector: detector}
	m.fingerprinter, _ = NewFingerprinter(make([]byte, 32))
	for _, encoded := range []string{value, base64.StdEncoding.EncodeToString([]byte(value))} {
		t.Run(encoded, func(t *testing.T) {
			raw := strings.Repeat("safe data\n", 48000) + " " + encoded + " " + strings.Repeat("ordinary inert line\n", 18000)
			want := m.RedactString(context.Background(), raw)
			var out bytes.Buffer
			result, err := m.ScreenBytes(t.Context(), strings.NewReader(raw), &out, "", StreamBounds{InputBytes: 64 << 20, OutputBytes: 64 << 20, MemoryBytes: 32 << 20, DetectorWork: 1 << 40}, nil, nil)
			if err != nil || out.String() != want || !result.Redacted {
				t.Fatalf("catalog policy differs: err=%v equal=%v result=%+v", err, out.String() == want, result)
			}
		})
	}
}

func TestOriginalRuneBoundaryKeepsInvalidBytesAndWholeRunes(t *testing.T) {
	for _, value := range []string{strings.Repeat("\x80", 100), strings.Repeat("λ", 50), strings.Repeat("🙂", 25)} {
		for at := 1; at < len(value); at++ {
			boundary := originalRuneBoundary(value, at)
			if boundary > at || boundary < at-3 {
				t.Fatalf("invalid boundary %d -> %d", at, boundary)
			}
		}
	}
	raw := strings.Repeat("\x80", 3<<20)
	windows := screenWindows(raw, MaxScreenWindowBytes, screenWindowOverlapBytes)
	if len(windows) < 3 || windows[len(windows)-1].end != len(raw) {
		t.Fatalf("binary windows did not progress: %v", windows)
	}
}
