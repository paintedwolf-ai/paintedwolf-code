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
)

func TestScratchSurveySeesWritesAfterEmptyInventory(t *testing.T) {
	root := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, cfgDir)
	scratchDir, err := scratch.New(cfgDir).Ensure("sess-1")
	testutil.FailErr(t, "ensure session scratch under configdir", err)
	tc := nativefixture.Context(root)
	tc.Host.SessionScratchDir = scratchDir
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

	scratchDir, err := scratch.New(cfgDir).Ensure("sess-1")
	testutil.FailErr(t, "ensure scratch", err)
	testutil.FailErr(t, "write scratch note", os.WriteFile(filepath.Join(scratchDir, "scratch-note.txt"), []byte("scratch-ok-needle\n"), 0o600))

	tc := nativefixture.Context(cfgDir)
	tc.Host.SessionScratchDir = scratchDir
	rootID := tc.Source.Roots[0].ID

	catalog := sourcecatalog.New()
	humanSnap, err := catalog.ObserveAudience(
		t.Context(),
		tc.Identity.ProjectID,
		[]sourcecatalog.Root{{ID: rootID, Path: cfgDir}},
		sourcecatalog.HumanAudience,
	)
	testutil.FailErr(t, "warm human catalog on cfgDir", err)
	var humanRel []string
	for _, e := range humanSnap.Entries(rootID) {
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

	// 1. Agent find on "." with human-warmed catalog must return 0 entries.
	findOut, err := finder.Run(t.Context(), map[string]any{"path": "."}, tc)
	testutil.FailErr(t, "agent find on cfgDir", err)
	if found := parseFindResponse(t, findOut); len(found.Results) != 0 {
		t.Fatalf("agent find on directly attached control dir exposed entries: %+v", found.Results)
	}

	// 2. Agent list_dir literal listing (max_depth=1) on "." must return 0 entries.
	listOut, err := listing.Run(t.Context(), map[string]any{"path": ".", "max_depth": 1, "include_hidden": true}, tc)
	testutil.FailErr(t, "agent list_dir max_depth=1 on cfgDir", err)
	if listed := decodeListDirOut(t, listOut); len(listed.Entries) != 0 {
		t.Fatalf("agent list_dir on directly attached control dir exposed entries: %+v", listed.Entries)
	}
	// Agent list_dir zoomed map on "." must fail read scope.
	if _, err := listing.Run(t.Context(), map[string]any{"path": "."}, tc); err == nil {
		t.Fatal("expected agent zoomed map list_dir on directly attached control dir to be denied")
	}

	// 3. Agent grep on "." for control-plane content must return 0 matches.
	grepOut, err := grepper.Run(t.Context(), map[string]any{"path": ".", "pattern": "secret-control-plane"}, tc)
	testutil.FailErr(t, "agent grep on cfgDir", err)
	if matches := nativefixture.GrepMatches(t, grepOut); len(matches) != 0 {
		t.Fatalf("agent grep on directly attached control dir exposed matches: %+v", matches)
	}

	// 4. Agent read, wc, and stat on control-plane files must all be denied.
	for _, target := range []string{"settings.yaml", "packs/custom.yaml", "api.token"} {
		if out, err := reader.Run(t.Context(), map[string]any{"path": target}, tc); err == nil && strings.Contains(out, "secret-control-plane") {
			t.Fatalf("agent read(%q) exposed control-plane content: %s", target, out)
		} else if err == nil {
			t.Fatalf("agent read(%q) unexpectedly succeeded: %s", target, out)
		}
		if out, err := wcTool.Run(t.Context(), map[string]any{"paths": []any{target}}, tc); err == nil {
			t.Fatalf("agent wc(%q) unexpectedly succeeded: %s", target, out)
		}
		if out, err := statTool.Run(t.Context(), map[string]any{"paths": []any{target}}, tc); err == nil {
			t.Fatalf("agent stat(%q) unexpectedly succeeded: %s", target, out)
		}
	}

	// 5. Meanwhile, @scratch at <cfgDir>/scratch/sess-1 remains discoverable and readable!
	scratchFindOut, err := finder.Run(t.Context(), map[string]any{"path": "@scratch"}, tc)
	testutil.FailErr(t, "find @scratch under cfgDir", err)
	if found := parseFindResponse(t, scratchFindOut); len(found.Results) != 1 || found.Results[0].Path != "@scratch/scratch-note.txt" {
		t.Fatalf("find @scratch = %+v, want [@scratch/scratch-note.txt]", found.Results)
	}
	scratchReadOut, err := reader.Run(t.Context(), map[string]any{"path": "@scratch/scratch-note.txt"}, tc)
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
