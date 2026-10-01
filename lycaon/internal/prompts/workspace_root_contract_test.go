package prompts_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRootKickTracksSessionPathResolution(t *testing.T) {
	a := projectroot.RootRef{ID: "a", Label: "source", Path: t.TempDir(), IsPrimary: true}
	b := projectroot.RootRef{ID: "b", Label: "assets", Path: t.TempDir()}
	before := []projectroot.RootRef{a, b}
	a.IsPrimary, b.IsPrimary = false, true
	for _, roots := range [][]projectroot.RootRef{{a, b}, {b}, nil} {
		for _, activeID := range []string{"", "a", "b", "removed"} {
			data := prompts.RootsChangedKickData(before, roots, activeID)
			abs, resolved, err := projectroot.ResolveAbs(roots, activeID, "file.txt")
			active, present := data["active_root"].(map[string]any)
			if err != nil {
				if present {
					t.Fatalf("announced active root for an unresolvable session: %v", active)
				}
				continue
			}
			if !present || active["id"] != resolved.ID || filepath.Join(active["path"].(string), "file.txt") != abs {
				t.Fatalf("active=%q prompt=%v resolver=%s/%s", activeID, active, resolved.ID, abs)
			}
			for _, root := range roots {
				qualified, _, err := projectroot.ResolveAbs(roots, activeID, "@"+root.Label+"/file.txt")
				testutil.FailErr(t, "resolve explicit root label", err)
				if qualified != filepath.Join(root.Path, "file.txt") {
					t.Fatalf("qualified path = %q", qualified)
				}
			}
		}
	}
}
