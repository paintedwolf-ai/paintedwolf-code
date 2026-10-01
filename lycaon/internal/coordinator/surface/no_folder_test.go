package surface

import (
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surfacecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolscope"
)

func TestFilterSurfaceToolsForRootCountZeroRoots(t *testing.T) {
	tools := []string{"task", "read", "grep", "pack_board", "web_search", "fetch_url", "wait", "promote_overlay"}
	got, err := FilterSurfaceToolsForRootCount(tools, 0)
	testutil.FailErr(t, "FilterSurfaceToolsForRootCount", err)
	want := map[string]bool{"task": true, "web_search": true, "fetch_url": true, "wait": true}
	if len(got) != len(want) {
		t.Fatalf("filtered tools = %v want keys %v", got, want)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("unexpected tool %q in 0-root filter output: %v", name, got)
		}
	}
	for _, forbidden := range []string{"read", "grep", "pack_board", "promote_overlay"} {
		for _, name := range got {
			if name == forbidden {
				t.Fatalf("forbidden tool %q present at 0 roots", forbidden)
			}
		}
	}
}

func TestFilterSurfaceToolsForRootCountFolderModePassthrough(t *testing.T) {
	tools := []string{"task", "read", "grep"}
	got, err := FilterSurfaceToolsForRootCount(tools, 1)
	testutil.FailErr(t, "FilterSurfaceToolsForRootCount", err)
	if len(got) != len(tools) {
		t.Fatalf("filtered tools = %v want passthrough %v", got, tools)
	}
}

func TestNoFolderAllowlistFromYAML(t *testing.T) {
	allow, err := toolscope.NoFolderAllowlist()
	testutil.FailErr(t, "NoFolderAllowlist", err)
	if len(allow) == 0 {
		t.Fatal("expected non-empty no-folder allowlist")
	}
	seen := make(map[string]bool, len(allow))
	for _, name := range allow {
		seen[name] = true
	}
	for _, required := range []string{"task", "web_search", "fetch_url", "wait"} {
		if !seen[required] {
			t.Fatalf("no-folder allowlist missing %q: %v", required, allow)
		}
	}
}

func TestCatalogSurfaceIDsSkipNoFolderAllowlist(t *testing.T) {
	catalog, err := surfacecatalog.Load()
	testutil.FailErr(t, "load surface catalog", err)
	for _, id := range catalog.SurfaceIDs() {
		if id == surfacecatalog.NoFolderAllowlistID {
			t.Fatal("no-folder allowlist appeared as a coordinator surface")
		}
	}
}
