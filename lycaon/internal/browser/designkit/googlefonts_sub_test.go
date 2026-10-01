package designkit

import (
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseGoogleFontFamiliesCSS2(t *testing.T) {
	u, err := url.Parse("https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap")
	testutil.FailErr(t, "parse css2 font URL", err)
	got := ParseGoogleFontFamilies(u)
	if len(got) != 2 || got[0] != "Inter" || got[1] != "JetBrains Mono" {
		t.Fatalf("got %#v", got)
	}
}

func TestParseGoogleFontFamiliesCSS1Pipe(t *testing.T) {
	u, err := url.Parse("https://fonts.googleapis.com/css?family=Inter:400,700|Roboto:300")
	testutil.FailErr(t, "parse css1 font URL", err)
	got := ParseGoogleFontFamilies(u)
	if len(got) != 2 || got[0] != "Inter" || got[1] != "Roboto" {
		t.Fatalf("got %#v", got)
	}
}

func TestGoogleFontsSubstituteCSSKnownAndUnknown(t *testing.T) {
	css := GoogleFontsSubstituteCSS([]string{"Inter", "Comic Sans", "JetBrains Mono"})
	if !strings.Contains(css, `font-family:"Inter"`) {
		t.Fatalf("missing Inter: %s", css)
	}
	if !strings.Contains(css, `font-family:"JetBrains Mono"`) {
		t.Fatalf("missing JetBrains: %s", css)
	}
	if !strings.Contains(css, KitOrigin+"fonts/Inter.woff2") {
		t.Fatalf("missing kit url: %s", css)
	}
	if !strings.Contains(css, `skipped unknown family "Comic Sans"`) {
		t.Fatalf("expected skip comment: %s", css)
	}
	if strings.Count(css, "@font-face") != 2 {
		t.Fatalf("want 2 @font-face, got %s", css)
	}
}

func TestLookupFontByCompactName(t *testing.T) {
	f, ok := LookupFontByCompactName("jetbrainsmono")
	if !ok || f.Family != "JetBrains Mono" {
		t.Fatalf("got %#v ok=%v", f, ok)
	}
	if _, ok := LookupFontByCompactName("comic-sans"); ok {
		t.Fatal("expected miss")
	}
}

func TestGstaticFamilySlug(t *testing.T) {
	if g := gstaticFamilySlug("/s/inter/v20/UcCO3.woff2"); g != "inter" {
		t.Fatalf("got %q", g)
	}
	if g := gstaticFamilySlug("/s/jetbrainsmono/v24/x.ttf"); g != "jetbrainsmono" {
		t.Fatalf("got %q", g)
	}
}
