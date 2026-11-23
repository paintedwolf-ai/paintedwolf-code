package contract

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/capability"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/nativemanifest"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolscope"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestFileToolFactHasOneSource(t *testing.T) {
	t.Parallel()
	allowlist, err := toolscope.NoFolderAllowlist()
	contractcheck.FailErr(t, "load no-folder allowlist", err)
	if len(allowlist) == 0 {
		t.Fatal("no_folder_allowlist is empty")
	}

	for _, rootCount := range []int{0, 1} {
		caps := capability.Derive(rootCount, allowlist, nil)
		if caps.HasFileTools {
			t.Errorf("has_file_tools is true at root_count=%d for no-folder allowlist (%s)",
				rootCount, strings.Join(allowlist, ", "))
		}
		if caps.HasWorkspaceRoots != (rootCount > 0) {
			t.Errorf("has_workspace_roots=%v at root_count=%d", caps.HasWorkspaceRoots, rootCount)
		}
	}

	cfg, loadErr := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", loadErr)
	folderRequiring := folderRequiringCatalogTools(cfg, allowlist)
	if len(folderRequiring) == 0 {
		t.Fatal("no catalog tool requires an attached folder — the axis is inert")
	}
	fileTools := 0
	for _, tool := range cfg.AllTools() {
		wantFileTool := prompts.IsProjectFileTool(tool)
		if wantFileTool {
			fileTools++
		}
		zero := capability.Derive(0, []string{tool}, nil)
		if zero.HasWorkspaceRoots || zero.HasFileTools {
			t.Errorf("root_count=0 set workspace/file flags for %q: %+v", tool, zero)
		}
		one := capability.Derive(1, []string{tool}, nil)
		if !one.HasWorkspaceRoots {
			t.Errorf("has_workspace_roots is false at root_count=1 for %q", tool)
		}
		if one.HasFileTools != wantFileTool {
			t.Errorf("has_file_tools=%v want %v for %q at root_count=1", one.HasFileTools, wantFileTool, tool)
		}
	}
	if fileTools == 0 {
		t.Fatal("no catalog tool matched IsProjectFileTool — has_file_tools is inert")
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

func folderRequiringCatalogTools(cfg nativemanifest.Config, allowlist []string) []string {
	var out []string
	for _, name := range cfg.AllTools() {
		if !slices.Contains(allowlist, name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
