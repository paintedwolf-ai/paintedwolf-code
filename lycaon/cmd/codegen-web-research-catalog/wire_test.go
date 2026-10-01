package main

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/webresearch"
)

func TestWireProviderIDsAppendsDirect(t *testing.T) {
	cat := stubProviderCatalog{providers: []webresearch.CatalogEntry{
		{ID: "brave"}, {ID: "wikipedia"},
	}}
	got := wireProviderIDs(cat)
	if len(got) != 3 || got[2] != "direct" {
		t.Fatalf("got %#v", got)
	}
}

func TestWebSearchProviderConstNameOverrides(t *testing.T) {
	cases := map[string]string{
		"brave":          "WebSearchProviderBrave",
		"google_cse":     "WebSearchProviderGoogleCSE",
		"hn":             "WebSearchProviderHN",
		"mdn":            "WebSearchProviderMDN",
		"crates_io":      "WebSearchProviderCratesIO",
		"discourse_rust": "WebSearchProviderDiscourseRust",
		"direct":         "WebSearchProviderDirect",
	}
	for id, want := range cases {
		got, err := webSearchProviderConstName(id)
		if err != nil || got != want {
			t.Fatalf("%s: got %q err=%v want %q", id, got, err, want)
		}
	}
}

func TestRenderOpenAPIAndGo(t *testing.T) {
	cat := stubProviderCatalog{providers: []webresearch.CatalogEntry{
		{ID: "brave", Kind: webresearch.KindKeyed, Label: "Brave", Hint: "h", TestQuery: "t"},
	}}
	openapi, err := renderOpenAPIEnum(cat)
	testutil.FailErr(t, "renderOpenAPIEnum", err)
	text := string(openapi)
	if !strings.Contains(text, "WebSearchProvider:") || !strings.Contains(text, "- brave") || !strings.Contains(text, "- direct") {
		t.Fatalf("openapi = %s", text)
	}
	goSrc, err := renderGoProviderIDs(cat)
	testutil.FailErr(t, "renderGoProviderIDs", err)
	goText := string(goSrc)
	if !strings.Contains(goText, "WebSearchProviderBrave") || !strings.Contains(goText, `= "direct"`) {
		t.Fatalf("go = %s", goText)
	}
}

type stubProviderCatalog struct {
	providers []webresearch.CatalogEntry
}

func (c stubProviderCatalog) Entries() []webresearch.CatalogEntry {
	return append([]webresearch.CatalogEntry(nil), c.providers...)
}

func (c stubProviderCatalog) IDs() []string {
	ids := make([]string, len(c.providers))
	for i, provider := range c.providers {
		ids[i] = provider.ID
	}
	return ids
}
