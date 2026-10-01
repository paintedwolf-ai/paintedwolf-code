package contract

import (
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/toolscope"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// "Can this turn reach project files" is one catalog fact read three ways: the
// coordinator surface hides folder-requiring tools at zero roots, the no_folder
// posture rule denies them, and prompt copy gates on has_file_tools. All resolve
// through toolscope.RequiresProjectRoots.
func TestFileToolFactHasOneSource(t *testing.T) {
	t.Parallel()
	allowlist, err := toolscope.NoFolderAllowlist()
	contractcheck.FailErr(t, "load no-folder allowlist", err)
	if len(allowlist) == 0 {
		t.Fatal("no_folder_allowlist is empty")
	}

	// Every allowlisted tool works without a folder, so a surface built only from
	// them must not claim file tools.
	caps := capability.Derive(0, allowlist, nil)
	if caps.HasFileTools {
		t.Errorf("has_file_tools is true for a surface that is exactly the "+
			"no-folder allowlist (%s)", strings.Join(allowlist, ", "))
	}

	// A tool the allowlist omits flips it at any root count: a profile can
	// withhold file tools with a folder attached.
	cfg, loadErr := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", loadErr)
	folderRequiring := folderRequiringCatalogTools(cfg)
	if len(folderRequiring) == 0 {
		t.Fatal("no catalog tool requires an attached folder — the axis is inert")
	}
	for _, tool := range folderRequiring {
		for _, rootCount := range []int{0, 1} {
			got := capability.Derive(rootCount, []string{tool}, nil)
			if !got.HasFileTools {
				t.Errorf("has_file_tools is false for %q at root_count=%d, but the "+
					"catalog keeps it off the no-folder allowlist", tool, rootCount)
			}
		}
	}
}

// At zero roots everything the surface filter keeps is callable without a
// folder, so nothing it keeps may set has_file_tools.
func TestZeroRootSurfaceNeverClaimsFileTools(t *testing.T) {
	t.Parallel()
	cfg, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)

	every := cfg.AllTools()
	sort.Strings(every)
	kept, filterErr := surface.FilterSurfaceToolsForRootCount(every, 0)
	contractcheck.FailErr(t, "filter surface tools at zero roots", filterErr)
	if len(kept) == 0 {
		t.Fatal("zero-root filter kept nothing — the comparison is vacuous")
	}
	if caps := capability.Derive(0, kept, nil); caps.HasFileTools {
		t.Errorf("the zero-root surface sets has_file_tools; kept = %s",
			strings.Join(kept, ", "))
	}
}

func folderRequiringCatalogTools(cfg nativemanifest.Config) []string {
	var out []string
	for _, name := range cfg.AllTools() {
		if toolscope.RequiresProjectRoots(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
