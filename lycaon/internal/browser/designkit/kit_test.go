package designkit

import (
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFontCatalogComplete(t *testing.T) {
	for _, f := range Fonts() {
		data, err := readEmbedded("fonts/" + f.File)
		if err != nil {
			t.Fatalf("font %s: %v", f.Family, err)
		}
		if len(data) < 1000 {
			t.Fatalf("font %s too small: %d", f.Family, len(data))
		}
	}
}

func TestDocumentHTMLIncludesKit(t *testing.T) {
	html, err := DocumentHTML(Options{
		Theme:    "dark",
		BodyHTML: `<h1 class="kit-font-display">Hello</h1><svg class="kit-icon"><use href="#kit-check"/></svg>`,
	})
	testutil.FailErr(t, "DocumentHTML failed", err)
	for _, want := range []string{
		`data-kit-theme="dark"`,
		`font-family:"Inter"`,
		`data:font/woff2;base64,`,
		`id="kit-check"`,
		`--kit-bg`,
		`kit-font-display`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("document missing %q", want)
		}
	}
	// Unreferenced icons stay out of the sprite.
	if strings.Contains(html, `id="kit-airplane"`) || strings.Count(html, `id="kit-`) > 5 {
		t.Fatalf("expected reference-scoped sprite, got oversized sprite")
	}
}

func TestIconCatalogExpanded(t *testing.T) {
	if len(Icons()) < 400 {
		t.Fatalf("expected expanded icon catalog, got %d", len(Icons()))
	}
	for _, name := range []string{"house", "triangle-alert", "search", "settings", "x", "trash-2", "mail"} {
		if _, ok := LookupIcon(name); !ok {
			t.Fatalf("missing icon %q", name)
		}
	}
	if _, ok := LookupIcon("close"); ok {
		t.Fatal("unexpected non-catalog icon")
	}
}

func TestValidateIconRefsSuggests(t *testing.T) {
	err := ValidateIconRefs(`<use href="#kit-search"/><use href="#kit-not-a-real-icon-zz"/>`)
	if err == nil {
		t.Fatal("expected unknown icon")
	}
	ire := &IconRefError{}
	ok := errors.As(err, &ire)
	if !ok || len(ire.Unknown) != 1 {
		t.Fatalf("got %#v", err)
	}
}

func TestIconGroupsNonEmpty(t *testing.T) {
	groups := IconGroups()
	if len(groups) < 8 {
		t.Fatalf("groups: %d", len(groups))
	}
	for _, g := range groups {
		if g.ID == "" || len(g.Icons) == 0 {
			t.Fatalf("empty group %#v", g)
		}
	}
}

func TestGetCatalogListsAssets(t *testing.T) {
	c := GetCatalog()
	if len(c.Fonts) != len(Fonts()) {
		t.Fatalf("fonts: %d", len(c.Fonts))
	}
	if len(c.Icons) < len(Icons()) {
		t.Fatalf("icons: %d", len(c.Icons))
	}
	if len(c.IconGroups) == 0 || c.IconNaming == "" || len(c.Icons) == 0 {
		t.Fatalf("catalog incomplete: groups=%d naming=%t icons=%d", len(c.IconGroups), c.IconNaming != "", len(c.Icons))
	}
	if len(c.Viewports) == 0 || len(c.Themes) != 3 {
		t.Fatalf("catalog incomplete: %#v", c)
	}
}

func TestFontFaceCSSInlinesWOFF2(t *testing.T) {
	css := FontFaceCSS()
	if !strings.Contains(css, `font-family:"Fraunces"`) {
		t.Fatal("missing Fraunces face")
	}
	if !strings.Contains(css, "data:font/woff2;base64,") {
		t.Fatal("expected inlined font data URL")
	}
	if strings.Contains(css, KitOrigin+"fonts/") {
		t.Fatal("font faces must not depend on kit origin fetch")
	}
}

func TestValidateFontsRejectsUnknown(t *testing.T) {
	if err := ValidateFonts([]string{"Inter", "Comic Sans"}); err == nil {
		t.Fatal("expected unknown font error")
	}
	if err := ValidateFonts([]string{"Inter", "Fraunces"}); err != nil {
		testutil.FailErr(t, "ValidateFonts failed", err)
	}
}

func TestLookupViewportPreset(t *testing.T) {
	p, ok := LookupViewportPreset("phone")
	if !ok || p.Width != 390 {
		t.Fatalf("phone preset: %#v", p)
	}
	if _, ok := LookupViewportPreset("watch"); ok {
		t.Fatal("expected missing preset")
	}
}

func TestIconsReferencedInMarkup(t *testing.T) {
	got := IconsReferencedInMarkup(`<use href="#kit-home"></use><use xlink:href="#kit-arrow-right"/>`)
	if len(got) != 2 || got[0] != "arrow-right" || got[1] != "home" {
		t.Fatalf("got %#v", got)
	}
}

func TestProvenanceEmbedded(t *testing.T) {
	data, err := readEmbedded("provenance.yaml")
	testutil.FailErr(t, "readEmbedded failed", err)
	text := string(data)
	for _, want := range []string{"OFL-1.1", "ISC", "Inter", "Lucide"} {
		if !strings.Contains(text, want) {
			t.Fatalf("provenance missing %q", want)
		}
	}
}
