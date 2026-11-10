package survey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestScratchSurveySeesWritesAfterEmptyInventory(t *testing.T) {
	root, scratch := t.TempDir(), t.TempDir()
	tc := nativefixture.Context(root)
	tc.Host.SessionScratchDir = scratch
	catalog := sourcecatalog.New()
	_, err := catalog.Observe(t.Context(), tc.Identity.ProjectID, []sourcecatalog.Root{{ID: projectroot.VirtualScratchLabel, Path: scratch}})
	testutil.FailErr(t, "observe empty scratch", err)
	testutil.FailErr(t, "write scratch script", os.WriteFile(filepath.Join(scratch, "test-app.mjs"), []byte("console.log('needle');\n"), 0o600))
	boundary := nativefixture.Boundary(t)
	finder := &FindTool{Boundary: boundary, Catalog: catalog}
	out, err := finder.Run(t.Context(), map[string]any{"path": "@scratch", "name_glob": "*.mjs"}, tc)
	testutil.FailErr(t, "find new scratch script", err)
	if found := parseFindResponse(t, out); len(found.Results) != 1 {
		t.Errorf("find missed scratch write: %s", out)
	}
	listing := &ListDirTool{Boundary: boundary, Catalog: catalog}
	out, err = listing.Run(t.Context(), map[string]any{"path": "@scratch"}, tc)
	testutil.FailErr(t, "list new scratch script", err)
	if found := decodeListDirOut(t, out); len(found.Entries) != 1 {
		t.Errorf("list_dir missed scratch write: %s", out)
	}
	grepper := &GrepTool{Boundary: boundary, Catalog: catalog}
	out, err = grepper.Run(t.Context(), map[string]any{"path": "@scratch", "pattern": "needle"}, tc)
	testutil.FailErr(t, "search new scratch script", err)
	if found := nativefixture.GrepMatches(t, out); len(found) != 1 {
		t.Errorf("grep missed scratch write: %s", out)
	}
}

type unavailableScratchInventory struct {
	sourceInventoryCatalog
	err error
}

func (c unavailableScratchInventory) ObserveDependencyScope(context.Context, sourcecatalog.Root) (sourcecatalog.Snapshot, error) {
	return sourcecatalog.Snapshot{}, c.err
}
func TestScratchInventoryPreservesObservationError(t *testing.T) {
	expected := errors.New("scratch observation unavailable")
	root := projectroot.RootRef{ID: projectroot.VirtualScratchLabel, Path: t.TempDir()}
	_, err := sourceInventoryForScope(t.Context(), unavailableScratchInventory{err: expected}, "project", root, root.Path)
	if !errors.Is(err, expected) {
		t.Fatalf("observation error = %v, want %v", err, expected)
	}
}
func TestScratchInventoryScopesFreshNestedFiles(t *testing.T) {
	root := projectroot.RootRef{ID: projectroot.VirtualScratchLabel, Path: t.TempDir()}
	writeInventoryTree(t, root.Path, "nested/a.go", "other/b.go")
	catalog := sourcecatalog.New()
	for _, count := range []int{1, 0} {
		if count == 0 {
			testutil.FailErr(t, "remove scratch file", os.Remove(filepath.Join(root.Path, "nested/a.go")))
		}
		inventory, err := sourceInventoryForScope(t.Context(), catalog, "project", root, filepath.Join(root.Path, "nested"))
		testutil.FailErr(t, "observe scratch subdirectory", err)
		if got := inventoryPaths(t, inventory); len(got) != count {
			t.Fatalf("scoped scratch paths = %v, want %d", got, count)
		}
	}
}

func TestAttachedControlPlaneRootSurveyWithWarmCatalogDeniesStateAndAdmitsScratch(t *testing.T) {
	home := fspath.CanonicalPath(t.TempDir())
	cfgDir := filepath.Join(home, ".config", "paintedwolf-state")
	ownScratch := filepath.Join(cfgDir, "scratch", "own")
	otherScratch := filepath.Join(cfgDir, "scratch", "other")
	ownOutput := filepath.Join(cfgDir, "session-output", "own")
	for _, dir := range []string{cfgDir, ownScratch, otherScratch, ownOutput} {
		testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(dir, 0o700))
	}
	testutil.FailErr(t, "write api.token", os.WriteFile(filepath.Join(cfgDir, "api.token"), []byte("needle-control-secret\n"), 0o600))
	testutil.FailErr(t, "write settings.yaml", os.WriteFile(filepath.Join(cfgDir, "settings.yaml"), []byte("needle-settings\n"), 0o600))
	testutil.FailErr(t, "write store.db", os.WriteFile(filepath.Join(cfgDir, "store.db"), []byte("needle-store\n"), 0o600))
	testutil.FailErr(t, "write other scratch", os.WriteFile(filepath.Join(otherScratch, "secret.mjs"), []byte("console.log('needle-other');\n"), 0o600))
	testutil.FailErr(t, "write own scratch", os.WriteFile(filepath.Join(ownScratch, "note.mjs"), []byte("console.log('needle-own');\n"), 0o600))
	testutil.FailErr(t, "write own output", os.WriteFile(filepath.Join(ownOutput, "report.txt"), []byte("needle-output\n"), 0o600))

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	tc := nativefixture.Context(cfgDir)
	tc.Host.SessionScratchDir = ownScratch
	tc.Host.SessionOutputDir = ownOutput

	// Pre-warm the catalog as the human/UI observer (which crawls with IncludeEngineOverlay: true).
	catalog := sourcecatalog.New()
	primaryRoot := tc.Source.Roots[0]
	snap, err := catalog.Observe(t.Context(), tc.Identity.ProjectID, []sourcecatalog.Root{{ID: primaryRoot.ID, Path: cfgDir}})
	testutil.FailErr(t, "warm catalog on control-plane root", err)
	if children, ok := snap.Listing(primaryRoot.ID, "."); !ok || len(children) == 0 {
		t.Fatalf("warm catalog did not index control-plane root: ok=%v children=%d", ok, len(children))
	}

	boundary := nativefixture.Boundary(t)
	sessionCtx := sandbox.WithSessionDirs(t.Context(), ownScratch, ownOutput)

	// 1. Direct listRootCatalog against the warm snapshot must refuse control-plane root and other-session scratch.
	listing := &ListDirTool{Boundary: boundary, Catalog: catalog}
	if entries, ok := listing.listRootCatalog(sessionCtx, tc, primaryRoot, cfgDir, 1, true); ok || len(entries) != 0 {
		t.Fatalf("listRootCatalog on attached configdir.Root() = (%+v, %v), want (nil, false)", entries, ok)
	}
	if entries, ok := listing.listRootCatalog(sessionCtx, tc, primaryRoot, otherScratch, 1, true); ok || len(entries) != 0 {
		t.Fatalf("listRootCatalog on other session scratch = (%+v, %v), want (nil, false)", entries, ok)
	}

	// 2. Agent list_dir on "." (zoomed map and literal listing) and "api.token" must refuse with SANDBOX_CONTROL_PLANE_DENIED.
	for _, args := range []map[string]any{
		{"path": "."},
		{"path": ".", "include_hidden": true},
		{"path": "scratch/other", "include_hidden": true},
	} {
		_, runErr := listing.Run(t.Context(), args, tc)
		var reject *toolrejection.ToolReject
		if !errors.As(runErr, &reject) || reject.Code != isolation.CodeControlPlaneDenied {
			t.Fatalf("list_dir(%v) err = %v, want %s", args, runErr, isolation.CodeControlPlaneDenied)
		}
	}

	// 3. Agent find and grep on "." (union discovery) must return 0 control-plane results even with the warm catalog.
	finder := &FindTool{Boundary: boundary, Catalog: catalog}
	findOut, err := finder.Run(t.Context(), map[string]any{"path": ".", "name_glob": "*"}, tc)
	testutil.FailErr(t, "find on .", err)
	if found := parseFindResponse(t, findOut); len(found.Results) != 0 {
		t.Fatalf("find(.) on attached configdir.Root() leaked entries: %s", findOut)
	}
	grepper := &GrepTool{Boundary: boundary, Catalog: catalog}
	grepOut, err := grepper.Run(t.Context(), map[string]any{"path": ".", "pattern": "needle"}, tc)
	testutil.FailErr(t, "grep on .", err)
	if matches := nativefixture.GrepMatches(t, grepOut); len(matches) != 0 {
		t.Fatalf("grep(.) on attached configdir.Root() leaked matches: %s", grepOut)
	}

	// 4. Meanwhile, @scratch and @output remain discoverable and searchable by the owning session.
	scratchListOut, err := listing.Run(t.Context(), map[string]any{"path": "@scratch", "include_hidden": true}, tc)
	testutil.FailErr(t, "list_dir @scratch", err)
	if got := decodeListDirOut(t, scratchListOut); len(got.Entries) != 1 || got.Entries[0].Name != "note.mjs" {
		t.Fatalf("list_dir @scratch = %s, want [note.mjs]", scratchListOut)
	}
	scratchFindOut, err := finder.Run(t.Context(), map[string]any{"path": "@scratch", "name_glob": "*.mjs"}, tc)
	testutil.FailErr(t, "find @scratch", err)
	if got := parseFindResponse(t, scratchFindOut); len(got.Results) != 1 || got.Results[0].Path != "@scratch/note.mjs" {
		t.Fatalf("find @scratch = %s, want [@scratch/note.mjs]", scratchFindOut)
	}
	scratchGrepOut, err := grepper.Run(t.Context(), map[string]any{"path": "@scratch", "pattern": "needle"}, tc)
	testutil.FailErr(t, "grep @scratch", err)
	if matches := nativefixture.GrepMatches(t, scratchGrepOut); len(matches) != 1 || !strings.Contains(scratchGrepOut, "needle-own") {
		t.Fatalf("grep @scratch = %s, want 1 match with needle-own", scratchGrepOut)
	}
	outputFindOut, err := finder.Run(t.Context(), map[string]any{"path": "@output", "name_glob": "*.txt"}, tc)
	testutil.FailErr(t, "find @output", err)
	if got := parseFindResponse(t, outputFindOut); len(got.Results) != 1 || got.Results[0].Path != "@output/report.txt" {
		t.Fatalf("find @output = %s, want [@output/report.txt]", outputFindOut)
	}
}
