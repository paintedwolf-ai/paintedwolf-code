package survey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestScratchSurveySeesWritesAfterEmptyInventory(t *testing.T) {
	root := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, cfgDir)
	dirs, err := scratch.New(cfgDir).Ensure("sess-1")
	testutil.FailErr(t, "ensure session scratch under configdir", err)
	scratchDir := dirs.Scratch
	tc := nativefixture.Context(root)
	tc.Host.SessionScratchDir = scratchDir
	tc.Host.SessionOutputDir = dirs.Output
	catalog := sourcecatalog.New()
	_, err = catalog.Observe(t.Context(), tc.Identity.ProjectID, []sourcecatalog.Root{{ID: projectroot.VirtualScratchLabel, Path: scratchDir}})
	testutil.FailErr(t, "observe empty scratch", err)
	testutil.FailErr(t, "write scratch script", os.WriteFile(filepath.Join(scratchDir, "test-app.mjs"), []byte("console.log('needle');\n"), 0o600))
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

func TestDirectControlRootAttachmentWithHumanWarmedCatalogCannotExposeStateToAgentTools(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	testutil.FailErr(t, "mkdir packs", os.MkdirAll(filepath.Join(cfgDir, "packs"), 0o755))
	testutil.FailErr(t, "write settings.yaml", os.WriteFile(filepath.Join(cfgDir, "settings.yaml"), []byte("model: secret-control-plane\n"), 0o600))
	testutil.FailErr(t, "write custom.yaml", os.WriteFile(filepath.Join(cfgDir, "packs", "custom.yaml"), []byte("pack: secret-control-plane\n"), 0o600))
	testutil.FailErr(t, "write api.token", os.WriteFile(filepath.Join(cfgDir, "api.token"), []byte("token-secret-control-plane\n"), 0o600))
	testutil.FailErr(t, "write paintedwolf.db", os.WriteFile(filepath.Join(cfgDir, "paintedwolf.db"), []byte("sqlite-bytes"), 0o600))

	dirs, err := scratch.New(cfgDir).Ensure("sess-1")
	testutil.FailErr(t, "ensure scratch", err)
	scratchDir := dirs.Scratch
	testutil.FailErr(t, "write scratch note", os.WriteFile(filepath.Join(scratchDir, "scratch-note.txt"), []byte("scratch-ok-needle\n"), 0o600))

	tc := nativefixture.Context(cfgDir)
	tc.Host.SessionScratchDir = scratchDir
	tc.Host.SessionOutputDir = dirs.Output
	rootID := tc.Source.Roots[0].ID

	catalog := sourcecatalog.New()
	catRoot := sourcecatalog.Root{ID: rootID, Path: cfgDir}
	humanSnap, err := catalog.Observe(
		t.Context(),
		tc.Identity.ProjectID,
		[]sourcecatalog.Root{catRoot},
	)
	testutil.FailErr(t, "warm human catalog on cfgDir", err)
	_, err = catalog.Directories.ObserveDirectory(t.Context(), tc.Identity.ProjectID, catRoot, ".", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "warm human directory index on cfgDir", err)
	var humanRel []string
	for _, e := range humanSnap.Entries {
		humanRel = append(humanRel, e.Path)
	}
	wantHuman := []string{"packs", "packs/custom.yaml", "settings.yaml"}
	if !reflect.DeepEqual(humanRel, wantHuman) {
		t.Fatalf("human catalog entries on cfgDir = %v, want %v", humanRel, wantHuman)
	}

	boundary := nativefixture.Boundary(t)
	finder := &FindTool{Boundary: boundary, Catalog: catalog}
	listing := &ListDirTool{Boundary: boundary, Catalog: catalog}
	grepper := &GrepTool{Boundary: boundary, Catalog: catalog}
	reader := &ReadTool{Boundary: boundary, Catalog: catalog}
	wcTool := &WcTool{Boundary: boundary, Catalog: catalog}
	statTool := &StatTool{Boundary: boundary, Catalog: catalog}
	rootRef := tc.Source.Roots[0]

	// 1. Direct survey/catalog inventory checks on human-warmed cfgDir:
	// Even when sourcecatalog holds a ready human snapshot for cfgDir, agent source
	// inventory, find walk, listRoot, wcRecursiveDir, and countDirEntries must
	// expose 0 control-plane entries.
	inv, err := sourceInventoryForScope(t.Context(), catalog, tc.Identity.ProjectID, rootRef, cfgDir)
	testutil.FailErr(t, "sourceInventoryForScope on human-warmed cfgDir", err)
	if got := inventoryPaths(t, inv); len(got) != 0 {
		t.Fatalf("agent sourceInventoryForScope on human-warmed cfgDir leaked: %v", got)
	}

	readFilter, err := boundary.CompileReadFilter(t.Context(), cfgDir, tc.ProfileID())
	testutil.FailErr(t, "CompileReadFilter on cfgDir", err)
	for _, surveyOnly := range []bool{false, true} {
		var resp findResponse
		var skipped, matchedTotal int
		var truncated, deeper bool
		p := findWalkParams{
			ctx:                t.Context(),
			tctx:               tc,
			root:               rootRef,
			fullRoot:           cfgDir,
			maxDepth:           8,
			maxResults:         100,
			entryType:          findTypeAny,
			resp:               &resp,
			skipped:            &skipped,
			matchedTotal:       &matchedTotal,
			truncated:          &truncated,
			deeperPathsOmitted: &deeper,
			catalog:            catalog,
			readFilter:         readFilter,
		}
		if surveyOnly {
			testutil.FailErr(t, "walkFindTreeSurvey on cfgDir", finder.walkFindTreeSurvey(p))
		} else {
			testutil.FailErr(t, "walkFindTree on cfgDir", finder.walkFindTree(p))
		}
		if len(resp.Results) != 0 || matchedTotal != 0 {
			t.Fatalf("find walk (surveyOnly=%v) on cfgDir leaked: %+v", surveyOnly, resp.Results)
		}
	}

	for _, depth := range []int{1, 2} {
		entries, err := listing.listRoot(t.Context(), tc, rootRef, cfgDir, ".", depth, true)
		testutil.FailErr(t, "listRoot on cfgDir", err)
		if len(entries) != 0 {
			t.Fatalf("listRoot(depth=%d) on human-warmed cfgDir leaked: %+v", depth, entries)
		}
	}

	reads := projectpaths.NewReadSession(boundary, tc)
	defer reads.Close()
	wcRes, err := wcTool.wcRecursiveDir(t.Context(), reads, tc, rootRef, cfgDir, ".", true, sourceview.DraftOverlay{})
	testutil.FailErr(t, "wcRecursiveDir on cfgDir", err)
	if wcRes.Files != nil && *wcRes.Files != 0 {
		t.Fatalf("wcRecursiveDir on human-warmed cfgDir counted %d files, want 0", *wcRes.Files)
	}
	wcSurveyRes, err := wcTool.wcRecursiveDirSurvey(t.Context(), reads, tc, rootRef, cfgDir, ".", true)
	testutil.FailErr(t, "wcRecursiveDirSurvey on cfgDir", err)
	if wcSurveyRes.Files != nil && *wcSurveyRes.Files != 0 {
		t.Fatalf("wcRecursiveDirSurvey on cfgDir counted %d files, want 0", *wcSurveyRes.Files)
	}
	if cnt, err := countDirEntries(t.Context(), boundary, tc, rootRef, cfgDir, true, "."); err != nil || cnt != 0 {
		t.Fatalf("countDirEntries on cfgDir = (%d, %v), want (0, nil)", cnt, err)
	}

	// 2. Top-level tool Run calls on attached cfgDir (must either reject or return 0 items, never leaking state):
	if findOut, err := finder.Run(t.Context(), map[string]any{"path": "."}, tc); err == nil {
		if found := parseFindResponse(t, findOut); len(found.Results) != 0 {
			t.Fatalf("agent find on directly attached control dir exposed entries: %+v", found.Results)
		}
	}
	if listOut, err := listing.Run(t.Context(), map[string]any{"path": ".", "max_depth": 1, "include_hidden": true}, tc); err == nil {
		if listed := decodeListDirOut(t, listOut); len(listed.Entries) != 0 {
			t.Fatalf("agent list_dir on directly attached control dir exposed entries: %+v", listed.Entries)
		}
	}
	if _, err := listing.Run(t.Context(), map[string]any{"path": "."}, tc); err == nil {
		t.Fatal("expected agent zoomed map list_dir on directly attached control dir to be denied")
	}
	if grepOut, err := grepper.Run(t.Context(), map[string]any{"path": ".", "pattern": "secret-control-plane"}, tc); err == nil {
		if matches := nativefixture.GrepMatches(t, grepOut); len(matches) != 0 {
			t.Fatalf("agent grep on directly attached control dir exposed matches: %+v", matches)
		}
	}
	for _, target := range []string{"settings.yaml", "packs/custom.yaml", "api.token"} {
		if out, err := reader.Run(t.Context(), map[string]any{"path": target}, tc); err == nil {
			t.Fatalf("agent read(%q) unexpectedly succeeded: %s", target, out)
		}
		if out, err := wcTool.Run(t.Context(), map[string]any{"paths": []any{target}}, tc); err == nil {
			t.Fatalf("agent wc(%q) unexpectedly succeeded: %s", target, out)
		}
		if out, err := statTool.Run(t.Context(), map[string]any{"paths": []any{target}}, tc); err == nil {
			t.Fatalf("agent stat(%q) unexpectedly succeeded: %s", target, out)
		}
	}

	// 3. Meanwhile, @scratch at <cfgDir>/scratch/sess-1 remains discoverable and readable!
	projTC := nativefixture.Context(t.TempDir())
	projTC.Host.SessionScratchDir = scratchDir
	projTC.Host.SessionOutputDir = dirs.Output
	scratchFindOut, err := finder.Run(t.Context(), map[string]any{"path": "@scratch"}, projTC)
	testutil.FailErr(t, "find @scratch under cfgDir", err)
	if found := parseFindResponse(t, scratchFindOut); len(found.Results) != 1 || found.Results[0].Path != "@scratch/scratch-note.txt" {
		t.Fatalf("find @scratch = %+v, want [@scratch/scratch-note.txt]", found.Results)
	}
	scratchListOut, err := listing.Run(t.Context(), map[string]any{"path": "@scratch"}, projTC)
	testutil.FailErr(t, "list_dir @scratch under cfgDir", err)
	if listed := decodeListDirOut(t, scratchListOut); len(listed.Entries) != 1 || listed.Entries[0].Name != "scratch-note.txt" {
		t.Fatalf("list_dir @scratch = %+v, want [scratch-note.txt]", listed.Entries)
	}
	scratchReadOut, err := reader.Run(t.Context(), map[string]any{"path": "@scratch/scratch-note.txt"}, projTC)
	testutil.FailErr(t, "read @scratch/scratch-note.txt", err)
	if !strings.Contains(scratchReadOut, "scratch-ok-needle") {
		t.Fatalf("read @scratch/scratch-note.txt missing expected content: %s", scratchReadOut)
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
