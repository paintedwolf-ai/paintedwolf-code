package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/survey"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSurveyCatalogStarterBundles(t *testing.T) {
	t.Parallel()
	dir := survey.CatalogDir()
	cat, err := survey.LoadCatalog(dir)
	contractcheck.FailErr(t, "LoadCatalog", err)

	wantIDs := []string{"api_routes", "layout_overview", "ssot_drift"}
	got := cat.BundleIDs()
	if len(got) != len(wantIDs) {
		t.Fatalf("bundle ids = %v want %v", got, wantIDs)
	}
	for i, id := range wantIDs {
		if got[i] != id {
			t.Fatalf("bundle ids = %v want %v", got, wantIDs)
		}
	}
	for _, id := range wantIDs {
		b, ok := cat.Bundles[id]
		if !ok {
			t.Fatalf("missing bundle %q", id)
		}
		for _, p := range b.Probes {
			switch p.Kind {
			case survey.ProbeGrep, survey.ProbeFind, survey.ProbeListDir:
			default:
				t.Fatalf("bundle %q probe %q: invalid kind %q", id, p.Label, p.Kind)
			}
		}
	}
}
