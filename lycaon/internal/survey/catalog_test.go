package survey_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/extpacks"

	"github.com/lycaon/lycaon/internal/survey"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadCatalogStarterBundles(t *testing.T) {
	dir := survey.CatalogDir()
	cat, err := survey.LoadCatalog(dir)
	testutil.FailErr(t, "LoadCatalog", err)

	want := []string{"api_routes", "layout_overview", "ssot_drift"}
	got := cat.BundleIDs()
	if len(got) != len(want) {
		t.Fatalf("bundle ids = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bundle ids = %v want %v", got, want)
		}
	}
	for id, bundle := range cat.Bundles {
		if len(bundle.Probes) == 0 {
			t.Fatalf("bundle %q has no probes", id)
		}
		for _, p := range bundle.Probes {
			switch p.Kind {
			case survey.ProbeGrep, survey.ProbeFind, survey.ProbeListDir:
			default:
				t.Fatalf("bundle %q probe %q: bad kind %q", id, p.Label, p.Kind)
			}
		}
	}
	api := cat.Bundles["api_routes"]
	labels := map[string]int{}
	for _, p := range api.Probes {
		labels[p.Label] = p.EmitCap
	}
	for _, wantLabel := range []string{"http_path_literals", "http_handler_regs", "router_constructors"} {
		cap, ok := labels[wantLabel]
		if !ok {
			t.Fatalf("api_routes missing probe %q; got %#v", wantLabel, labels)
		}
		if cap <= 0 {
			t.Fatalf("api_routes probe %q emit_cap = %d want > 0", wantLabel, cap)
		}
	}
	if _, ok := labels["router_wiring"]; ok {
		t.Fatal("api_routes still has broad router_wiring probe")
	}
}

func TestMergeOverlayAppendOnly(t *testing.T) {
	cat := &survey.Catalog{Bundles: map[string]survey.Bundle{
		"custom": {
			ID: "custom",
			Probes: []survey.Probe{{
				Kind: survey.ProbeGrep, Pattern: "foo", Path: ".", Label: "base", Priority: 1,
			}},
		},
	}}
	overlayDir := t.TempDir()
	yaml := `id: custom
probes:
  - kind: grep
    pattern: "bar"
    path: "."
    label: overlay
    priority: 2
`
	if err := os.WriteFile(filepath.Join(overlayDir, "custom.yaml"), []byte(yaml), 0o644); err != nil {
		testutil.FailErr(t, "write overlay", err)
	}
	if err := survey.MergeOverlay(cat, extpacks.OnDisk(overlayDir)); err != nil {
		testutil.FailErr(t, "MergeOverlay", err)
	}
	b := cat.Bundles["custom"]
	if len(b.Probes) != 2 {
		t.Fatalf("probes = %d want 2", len(b.Probes))
	}
	if b.Probes[1].Label != "overlay" {
		t.Fatalf("second probe = %+v", b.Probes[1])
	}
}
