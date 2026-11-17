package survey

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/summarize"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// Summary windows carry structured identity when they can; the session folder
// only decides legacy display strings.
func TestResolveSummaryWindowPrefersStructuredIdentity(t *testing.T) {
	base := t.TempDir()
	primary, secondary := filepath.Join(base, "app"), filepath.Join(base, "den")
	testutil.FailErr(t, "mkdir roots", os.MkdirAll(primary, 0o750))
	testutil.FailErr(t, "mkdir roots", os.MkdirAll(secondary, 0o750))
	testutil.FailErr(t, "seed primary", os.WriteFile(filepath.Join(primary, "notes.txt"), []byte("primary\n"), 0o600))
	testutil.FailErr(t, "seed secondary", os.WriteFile(filepath.Join(secondary, "notes.txt"), []byte("secondary\n"), 0o600))
	tctx := testfixture.Context(primary)
	tctx.Source.Roots = []projectroot.RootRef{
		{ID: "primary", Label: "app", Path: primary, IsPrimary: true},
		{ID: "active", Label: "den", Path: secondary},
	}
	tctx.Source.ActiveRootID = "active"
	resolved, ok := resolveSummaryWindow(tctx, summarize.PackWindow{RootID: "active", ScopeRel: "notes.txt", Path: "@den/notes.txt"})
	if !ok || resolved.Root.ID != "active" || resolved.ScopeRel != "notes.txt" {
		t.Fatalf("structured window = %+v ok=%v", resolved, ok)
	}

	if _, ok := resolveSummaryWindow(tctx, summarize.PackWindow{RootID: "missing", ScopeRel: "notes.txt", Path: "notes.txt"}); ok {
		t.Fatal("unknown structured root resolved anyway")
	}

	if _, ok := resolveSummaryWindow(tctx, summarize.PackWindow{Path: "notes.txt"}); ok {
		t.Fatal("a window without root identity resolved by its display path")
	}
}
