package main

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func validDoc() catalogDoc {
	doc := catalogDoc{CatalogVersion: 1}
	doc.Fallbacks.UI = "system-ui, sans-serif"
	doc.Fallbacks.Mono = "ui-monospace, monospace"
	doc.Families = []catalogFamily{
		{Family: "Inter", Role: "ui", Default: true, Description: "Neutral."},
		{Family: "JetBrains Mono", Role: "mono", Default: true, Description: "For code."},
	}
	return doc
}

func TestResolveFamiliesJoinsThePack(t *testing.T) {
	got, err := resolveFamilies(validDoc())
	testutil.FailErr(t, "resolveFamilies failed", err)

	if len(got) != 2 {
		t.Fatalf("want 2 families, got %d", len(got))
	}
	if got[0].File != "Inter.woff2" {
		t.Fatalf("Inter file: %q", got[0].File)
	}
	if got[0].LicenseFile != "licenses/OFL-inter.txt" || got[0].Copyright == "" {
		t.Fatalf("Inter provenance: %#v", got[0])
	}
}

func TestResolveFamiliesRejects(t *testing.T) {
	cases := []struct {
		name  string
		muts  func(*catalogDoc)
		wants string
	}{
		{
			name:  "family outside the pack",
			muts:  func(d *catalogDoc) { d.Families[0].Family = "Comic Sans MS" },
			wants: "not in the designkit pack",
		},
		{
			name:  "family misspelled against the pack",
			muts:  func(d *catalogDoc) { d.Families[0].Family = "inter" },
			wants: "is spelled",
		},
		{
			name:  "unknown role",
			muts:  func(d *catalogDoc) { d.Families[0].Role = "display" },
			wants: "role must be ui or mono",
		},
		{
			name:  "missing description",
			muts:  func(d *catalogDoc) { d.Families[0].Description = "  " },
			wants: "description is required",
		},
		{
			name: "duplicate family",
			muts: func(d *catalogDoc) {
				d.Families = append(d.Families, d.Families[0])
			},
			wants: "listed twice",
		},
		{
			name: "two defaults in one role",
			muts: func(d *catalogDoc) {
				d.Families = append(d.Families, catalogFamily{
					Family: "DM Sans", Role: "ui", Default: true, Description: "Geometric.",
				})
			},
			wants: "two defaults",
		},
		{
			name:  "role with no default",
			muts:  func(d *catalogDoc) { d.Families[1].Default = false },
			wants: "no default family",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := validDoc()
			tc.muts(&doc)
			_, err := resolveFamilies(doc)
			if err == nil {
				t.Fatal("expected a rejection")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("want error containing %q, got %v", tc.wants, err)
			}
		})
	}
}

func TestFaceStylesheetDeclaresEveryFamily(t *testing.T) {
	families, err := resolveFamilies(validDoc())
	testutil.FailErr(t, "resolveFamilies failed", err)
	css := string(faceStylesheet(validDoc(), families))

	if strings.Count(css, "@font-face") != 2 {
		t.Fatalf("want 2 @font-face rules, got %s", css)
	}
	// The bundle serves these from the app origin; CSP is `font-src 'self'`, so
	// an absolute app-origin path is the only form that loads.
	if !strings.Contains(css, `src: url("/fonts/Inter.woff2") format("woff2")`) {
		t.Fatalf("Inter src missing from %s", css)
	}
	if !strings.Contains(css, "font-weight: 100 900") {
		t.Fatal("variable weight range missing — every weight would synthesise")
	}
	// A first launch has no stored preference or boot memo; the cold-start tokens
	// paint the product type before app state loads.
	if !strings.Contains(css, `--den-font-ui: "Inter", system-ui, sans-serif;`) {
		t.Fatalf("cold-start ui token missing from %s", css)
	}
	if !strings.Contains(css, `--den-font-mono: "JetBrains Mono", ui-monospace, monospace;`) {
		t.Fatalf("cold-start mono token missing from %s", css)
	}
}

func TestCatalogModuleCarriesStacksAndDefaults(t *testing.T) {
	doc := validDoc()
	families, err := resolveFamilies(doc)
	testutil.FailErr(t, "resolveFamilies failed", err)
	ts := string(catalogModule(doc, families))

	// A multi-word family unquoted in the stack parses as separate names.
	if !strings.Contains(ts, `stack: "\"JetBrains Mono\", ui-monospace, monospace"`) {
		t.Fatalf("mono stack missing or unquoted in %s", ts)
	}
	if !strings.Contains(ts, `ui: "Inter"`) || !strings.Contains(ts, `mono: "JetBrains Mono"`) {
		t.Fatalf("defaults missing from %s", ts)
	}
	// The latin subset cannot carry every script; the platform stack behind it
	// renders CJK and other non-latin text.
	if !strings.Contains(ts, "DEN_FONT_FALLBACKS") {
		t.Fatal("fallback stacks missing")
	}
}

func TestQuoteFamilyEscapes(t *testing.T) {
	if got := quoteFamily(`Say "Hi"`); got != `"Say \"Hi\""` {
		t.Fatalf("got %s", got)
	}
}
