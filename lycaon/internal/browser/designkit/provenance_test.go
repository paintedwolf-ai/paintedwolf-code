package designkit

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// Catalog and provenance entries stay one-to-one.
func TestProvenanceCoversCatalogExactly(t *testing.T) {
	byFamily, err := loadProvenance()
	testutil.FailErr(t, "loadProvenance failed", err)

	for _, f := range Fonts() {
		entry, ok := byFamily[f.Family]
		if !ok {
			t.Fatalf("font %q has no provenance entry", f.Family)
		}
		if entry.File != "fonts/"+f.File {
			t.Fatalf("font %q: provenance file %q, catalog file %q", f.Family, entry.File, f.File)
		}
		if entry.License == "" || entry.LicenseFile == "" || entry.Copyright == "" {
			t.Fatalf("font %q: incomplete provenance %#v", f.Family, entry)
		}
		if _, err := readEmbedded(entry.LicenseFile); err != nil {
			t.Fatalf("font %q: licence %q not embedded: %v", f.Family, entry.LicenseFile, err)
		}
	}

	if len(byFamily) != len(Fonts()) {
		t.Fatalf("provenance lists %d fonts, catalog has %d", len(byFamily), len(Fonts()))
	}
}

func TestFontProvenanceForUnknownFamily(t *testing.T) {
	if _, err := FontProvenanceFor("Comic Sans MS"); err == nil {
		t.Fatal("expected an error for a family outside the catalog")
	}
}

func TestFontProvenancePermitsRedistribution(t *testing.T) {
	for _, f := range Fonts() {
		entry, err := FontProvenanceFor(f.Family)
		testutil.FailErr(t, "FontProvenanceFor failed", err)
		// Bundled fonts require redistribution-friendly licensing.
		if !strings.HasPrefix(entry.License, "OFL-") {
			t.Fatalf("font %q licence %q is not OFL", f.Family, entry.License)
		}
	}
}
