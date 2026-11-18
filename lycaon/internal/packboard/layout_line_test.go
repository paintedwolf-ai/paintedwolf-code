package packboard_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFormatLayoutLineTinyFiles(t *testing.T) {
	line := packboard.FormatLayoutLine(api.RepoBrief{
		FileCount: 2,
		Layout: api.RepoLayout{
			Files: []string{"entropy-tictactoe.html", "HOW_TO_PLAY.md"},
		},
	})
	if line != "Files: entropy-tictactoe.html, HOW_TO_PLAY.md" {
		t.Fatalf("line = %q", line)
	}
}

func TestFormatLayoutLineSmallTopLevel(t *testing.T) {
	line := packboard.FormatLayoutLine(api.RepoBrief{
		FileCount: 24,
		Layout: api.RepoLayout{
			TopLevel: []string{"cmd/", "docs/", "go.mod", "internal/", "Makefile", "pkg/", "README.md", "scripts/"},
		},
	})
	if !strings.HasPrefix(line, "Top-level: ") {
		t.Fatalf("line = %q", line)
	}
	for _, want := range []string{"cmd/", "README.md", "go.mod"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line = %q missing %q", line, want)
		}
	}
}

func TestFormatLayoutLineQualityFloorDropsShortCoverage(t *testing.T) {
	names := make([]string, 8)
	for i := range names {
		names[i] = strings.Repeat("x", 58) + string(rune('a'+i))
	}
	line := packboard.FormatLayoutLine(api.RepoBrief{
		FileCount: 8,
		Layout:    api.RepoLayout{Files: names},
	})
	if line != "" {
		t.Fatalf("expected quality-floor drop, got %q", line)
	}
}

func TestFormatLayoutLinePerNameCapDropsLongPaths(t *testing.T) {
	line := packboard.FormatLayoutLine(api.RepoBrief{
		FileCount: 3,
		Layout: api.RepoLayout{
			Files: []string{
				strings.Repeat("a", 80),
				strings.Repeat("b", 80),
				strings.Repeat("c", 80),
			},
		},
	})
	if line != "" {
		t.Fatalf("line = %q want empty when no name fits cap", line)
	}
}

func TestFormatLayoutLineTruncatesWithMore(t *testing.T) {
	names := make([]string, 30)
	for i := range names {
		names[i] = "component-" + itoa(i) + "-dir/"
	}
	line := packboard.FormatLayoutLine(api.RepoBrief{
		FileCount: 30,
		Layout:    api.RepoLayout{TopLevel: names},
	})
	if !strings.Contains(line, "+") || !strings.Contains(line, "more") {
		t.Fatalf("line = %q want +M more suffix", line)
	}
	if len(line) > packboard.MaxLayoutLineBytes {
		t.Fatalf("line len = %d exceeds budget %d", len(line), packboard.MaxLayoutLineBytes)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// The orientation carries the repo brief once and the tier's layout list,
// never the other tier's list.
func TestBuildOrientationLinesCarryTierLayout(t *testing.T) {
	const language = "SentinelLang"
	files := []string{"orient-file-a", "orient-file-b"}
	topLevel := []string{"orient-top-a/", "orient-top-b/"}
	cases := []struct {
		count  int
		shown  []string
		hidden []string
	}{
		{2, files, topLevel},
		{12, topLevel, files},
		{packboard.MediumMaxFiles, topLevel, files},
		{packboard.MediumMaxFiles + 1, nil, append(append([]string(nil), files...), topLevel...)},
	}
	for _, tc := range cases {
		lines := packboard.BuildOrientationLines(api.BoardSnapshot{Repo: api.RepoBrief{
			FileCount: tc.count,
			Languages: []string{language},
			Layout:    api.RepoLayout{Files: files, TopLevel: topLevel},
		}}, packboard.OrientOpts{})
		linesWith := func(token string) int {
			n := 0
			for _, line := range lines {
				if strings.Contains(line, token) {
					n++
				}
			}
			return n
		}
		if got := linesWith(language); got != 1 {
			t.Errorf("%d files: %d lines carry the language, want 1: %q", tc.count, got, lines)
		}
		for _, name := range tc.shown {
			if got := linesWith(name); got != 1 {
				t.Errorf("%d files: %d lines carry %s, want 1: %q", tc.count, got, name, lines)
			}
		}
		for _, name := range tc.hidden {
			if got := linesWith(name); got != 0 {
				t.Errorf("%d files: %d lines carry %s, want 0: %q", tc.count, got, name, lines)
			}
		}
	}
}

func TestFitOrientationBudgetDropsLayoutBeforeTail(t *testing.T) {
	lines := []string{
		"Now: 2026-06-04 12:00 UTC",
		"Repo: 2 files · Go",
		"Files: a.go, b.go, c.go, d.go, e.go, f.go, g.go, h.go",
		"Git: main · clean",
	}
	fitted, dropped := packboard.FitOrientationBudget(lines, 80, nil)
	body := strings.Join(fitted, "\n")
	if len(body) > 80 {
		t.Fatalf("len=%d body=%q", len(body), body)
	}
	for _, line := range fitted {
		if strings.HasPrefix(line, "Files: ") {
			t.Fatalf("layout line should drop first: %v", fitted)
		}
	}
	if !dropped {
		t.Fatal("expected dropped=true")
	}
}

func TestOrientationFingerprintIncludesLayoutLine(t *testing.T) {
	base := api.BoardSnapshot{
		Repo: api.RepoBrief{
			Languages: []string{"Go"},
			FileCount: 2,
			Layout: api.RepoLayout{
				Files: []string{"main.go", "util.go"},
			},
		},
	}
	withLayout := packboard.OrientationFingerprint(base)
	changed := base
	changed.Repo.Layout.Files = []string{"main.go", "other.go"}
	if withLayout == packboard.OrientationFingerprint(changed) {
		t.Fatal("fingerprint should change when layout line changes")
	}
}
