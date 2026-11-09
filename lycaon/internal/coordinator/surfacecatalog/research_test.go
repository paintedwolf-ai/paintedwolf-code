package surfacecatalog

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestResearchSurfacesOfferDiscoveryAndReadingTogether(t *testing.T) {
	catalog, err := Load()
	testutil.FailErr(t, "load surfaces", err)
	for _, id := range catalog.SurfaceIDs() {
		row, err := catalog.Surface(id)
		testutil.FailErr(t, "read surface", err)
		for _, names := range [][]string{row.FloorTools(), row.LoadableTools()} {
			if contains(names, "web_search") != contains(names, "fetch_url") {
				t.Fatalf("surface %s separates research discovery and reading: %v", id, names)
			}
		}
	}
}
