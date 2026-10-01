package blueprint

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

func TestSlugTitleTruncatesUnicodeOnRuneBoundary(t *testing.T) {
	title := "a" + strings.Repeat("界", 16)
	slug := SlugTitle(title)
	if !utf8.ValidString(slug) {
		t.Fatalf("slug is not valid UTF-8: %q", slug)
	}
	if len(slug) > 48 {
		t.Fatalf("slug length = %d want at most 48 bytes", len(slug))
	}
	if slug != "a"+strings.Repeat("界", 15) {
		t.Fatalf("slug = %q", slug)
	}
}

func TestMintStemUsesDayWhenUnnamed(t *testing.T) {
	day := time.Date(2026, 8, 16, 15, 12, 0, 0, time.UTC)
	if got := MintStem("", day); got != "2026-08-16" {
		t.Fatalf("empty title stem = %q", got)
	}
	if got := MintStem("blueprint", day); got != "2026-08-16" {
		t.Fatalf("placeholder stem = %q", got)
	}
	if got := MintStem("Add OAuth", day); got != "add-oauth" {
		t.Fatalf("declared stem = %q", got)
	}
}

func TestMintUniquePathNumericCollision(t *testing.T) {
	day := time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)
	taken := map[string]bool{
		settingsoverlay.Rel("blueprints/add-oauth.md"): true,
	}
	got := MintUniquePath("Add OAuth", day, func(path string) bool { return taken[path] })
	want := settingsoverlay.Rel("blueprints/add-oauth-2.md")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDeclaredTitleSlugIsNotProvisional(t *testing.T) {
	if got := MintStem("Blueprint 2", time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC)); got != "blueprint-2" {
		t.Fatalf("stem = %q", got)
	}
	if IsProvisionalPath(settingsoverlay.Rel("blueprints/blueprint-2.md")) {
		t.Fatal("declared slug blueprint-2 must lock")
	}
}

func TestIsProvisionalPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{settingsoverlay.Rel("blueprints/2026-08-16.md"), true},
		{settingsoverlay.Rel("blueprints/2026-08-16-2.md"), true},
		{settingsoverlay.Rel("blueprints/blueprint.md"), true},
		{settingsoverlay.Rel("blueprints/plan.md"), true},
		{settingsoverlay.Rel("blueprints/blueprint-2.md"), false},
		{settingsoverlay.Rel("blueprints/plan-2.md"), false},
		{settingsoverlay.Rel("blueprints/add-oauth.md"), false},
		{settingsoverlay.Rel("blueprints/add-oauth-2.md"), false},
	}
	for _, tc := range cases {
		if got := IsProvisionalPath(tc.path); got != tc.want {
			t.Errorf("IsProvisionalPath(%q) = %v want %v", tc.path, got, tc.want)
		}
	}
}
