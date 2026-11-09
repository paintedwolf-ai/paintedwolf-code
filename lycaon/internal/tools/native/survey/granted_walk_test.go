package survey

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// A granted outside-root directory walks without dropping entries, and its
// entries render as canonical absolute paths (INV-WR-05).
func TestListDirGrantedOutsideRootWalkDropsNoEntries(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "lycaon")
	grant := filepath.Join(base, "approved-assets")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(filepath.Join(primary, "src"), 0o755))
	testutil.FailErr(t, "mkdir grant", os.MkdirAll(filepath.Join(grant, "nested"), 0o755))
	testutil.FailErr(t, "write grant file", os.WriteFile(filepath.Join(grant, "logo.txt"), []byte("x"), 0o644))
	testutil.FailErr(t, "write grant nested", os.WriteFile(filepath.Join(grant, "nested", "deep.txt"), []byte("x"), 0o644))

	tctx := nativefixture.Context(primary)
	release := projectpaths.SetGrantedAccessSource(func(_, _, abs string, _ bool) (projectpaths.Access, bool) {
		if abs == grant || filepath.HasPrefix(abs, grant+string(filepath.Separator)) {
			return projectpaths.Access{Path: grant, Tree: true}, true
		}
		return projectpaths.Access{}, false
	})
	t.Cleanup(release)

	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{"path": grant, "max_depth": 3}, tctx)
	testutil.FailErr(t, "list_dir granted tree", err)
	var resp struct {
		Entries []struct {
			Path string `json:"path"`
			Name string `json:"name"`
		} `json:"entries"`
	}
	testutil.FailErr(t, "decode list_dir resp", json.Unmarshal([]byte(nativefixture.SurveyContent(t, out)), &resp))
	if len(resp.Entries) != 3 {
		t.Fatalf("entries = %d want 3 (logo.txt, nested, nested/deep.txt): %+v", len(resp.Entries), resp.Entries)
	}
	want := map[string]bool{
		filepath.ToSlash(fspath.CanonicalPath(filepath.Join(grant, "logo.txt"))):        true,
		filepath.ToSlash(fspath.CanonicalPath(filepath.Join(grant, "nested"))):          true,
		filepath.ToSlash(fspath.CanonicalPath(filepath.Join(grant, "nested/deep.txt"))): true,
	}
	for _, entry := range resp.Entries {
		if !want[entry.Path] {
			t.Fatalf("entry path %q is not the canonical absolute spelling (want one of %v)", entry.Path, want)
		}
	}
}
