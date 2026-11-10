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
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
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
	obsCtx := sandbox.WithSessionDirs(t.Context(), scratchDir, dirs.Output)
	_, err = catalog.Observe(obsCtx, tc.Identity.ProjectID, []sourcecatalog.Root{{ID: projectroot.VirtualScratchLabel, Path: scratchDir}})
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

	// 4. Directly attaching another session's scratch directory (<cfgDir>/scratch/other-session)
	// as a project root while invocation owns sess-1 MUST NOT expose other-session files!
	otherDirs, err := scratch.New(cfgDir).Ensure("other-session")
	testutil.FailErr(t, "ensure other-session scratch", err)
	testutil.FailErr(t, "write other-session note", os.WriteFile(filepath.Join(otherDirs.Scratch, "other-secret.txt"), []byte("other-session-secret-needle\n"), 0o600))
	otherTC := nativefixture.Context(otherDirs.Scratch)
	otherTC.Host.SessionScratchDir = scratchDir
	otherTC.Host.SessionOutputDir = dirs.Output
	if findOut, err := finder.Run(t.Context(), map[string]any{"path": "."}, otherTC); err == nil {
		if found := parseFindResponse(t, findOut); len(found.Results) != 0 {
			t.Fatalf("agent find on other-session scratch root exposed entries: %+v", found.Results)
		}
	}
	if listOut, err := listing.Run(t.Context(), map[string]any{"path": ".", "max_depth": 1, "include_hidden": true}, otherTC); err == nil {
		if listed := decodeListDirOut(t, listOut); len(listed.Entries) != 0 {
			t.Fatalf("agent list_dir on other-session scratch root exposed entries: %+v", listed.Entries)
		}
	}
	if grepOut, err := grepper.Run(t.Context(), map[string]any{"path": ".", "pattern": "other-session-secret-needle"}, otherTC); err == nil {
		if matches := nativefixture.GrepMatches(t, grepOut); len(matches) != 0 {
			t.Fatalf("agent grep on other-session scratch root exposed matches: %+v", matches)
		}
	}
	if out, err := reader.Run(t.Context(), map[string]any{"path": "other-secret.txt"}, otherTC); err == nil {
		t.Fatalf("agent read on other-session scratch root unexpectedly succeeded: %s", out)
	}
	if out, err := wcTool.Run(t.Context(), map[string]any{"paths": []any{"other-secret.txt"}}, otherTC); err == nil {
		t.Fatalf("agent wc on other-session scratch root unexpectedly succeeded: %s", out)
	}
	if out, err := statTool.Run(t.Context(), map[string]any{"paths": []any{"other-secret.txt"}}, otherTC); err == nil {
		t.Fatalf("agent stat on other-session scratch root unexpectedly succeeded: %s", out)
	}

	// 5. Managed workspace (<cfgDir>/session-worktrees/proj-1/sess-1) with human-warmed catalog explicitly covered
	// by LYCAON_SANDBOX_DENY_READ (both subtree and whole-root) must be denied across discovery and read tools.
	wtDir := filepath.Join(cfgDir, "session-worktrees", "proj-1", "sess-1")
	testutil.FailErr(t, "mkdir wt/private", os.MkdirAll(filepath.Join(wtDir, "private"), 0o755))
	testutil.FailErr(t, "write wt/public.go", os.WriteFile(filepath.Join(wtDir, "public.go"), []byte("package main // wt-public-needle\n"), 0o600))
	testutil.FailErr(t, "write wt/private/secret.go", os.WriteFile(filepath.Join(wtDir, "private", "secret.go"), []byte("package private // wt-secret-needle\n"), 0o600))
	wtTC := nativefixture.Context(wtDir)
	wtTC.Host.SessionScratchDir = scratchDir
	wtTC.Host.SessionOutputDir = dirs.Output
	wtCatRoot := sourcecatalog.Root{ID: wtTC.Source.Roots[0].ID, Path: wtDir}
	_, err = catalog.Observe(t.Context(), wtTC.Identity.ProjectID, []sourcecatalog.Root{wtCatRoot})
	testutil.FailErr(t, "warm human catalog on wtDir", err)
	_, err = catalog.Directories.ObserveDirectory(t.Context(), wtTC.Identity.ProjectID, wtCatRoot, ".", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "warm human directory index on wtDir", err)

	// 5a. Subtree deny on wt-1/private:
	t.Setenv("LYCAON_SANDBOX_DENY_READ", filepath.Join(wtDir, "private"))
	wtFindSub, err := finder.Run(t.Context(), map[string]any{"path": "."}, wtTC)
	testutil.FailErr(t, "find on subtree-denied wtDir", err)
	if found := parseFindResponse(t, wtFindSub); len(found.Results) != 1 || found.Results[0].Path != "public.go" {
		t.Fatalf("find on subtree-denied wtDir = %+v, want [public.go]", found.Results)
	}
	wtListSub, err := listing.Run(t.Context(), map[string]any{"path": ".", "max_depth": 1, "include_hidden": true}, wtTC)
	testutil.FailErr(t, "list_dir on subtree-denied wtDir", err)
	if listed := decodeListDirOut(t, wtListSub); len(listed.Entries) != 1 || listed.Entries[0].Name != "public.go" {
		t.Fatalf("list_dir on subtree-denied wtDir = %+v, want [public.go]", listed.Entries)
	}
	if grepOut, err := grepper.Run(t.Context(), map[string]any{"path": ".", "pattern": "wt-secret-needle"}, wtTC); err == nil {
		if matches := nativefixture.GrepMatches(t, grepOut); len(matches) != 0 {
			t.Fatalf("grep on subtree-denied wtDir leaked private/secret.go: %+v", matches)
		}
	}
	if out, err := reader.Run(t.Context(), map[string]any{"path": "private/secret.go"}, wtTC); err == nil {
		t.Fatalf("read(private/secret.go) on subtree-denied wtDir unexpectedly succeeded: %s", out)
	}

	// 5b. Whole-root deny on wt-1:
	t.Setenv("LYCAON_SANDBOX_DENY_READ", wtDir)
	if findOut, err := finder.Run(t.Context(), map[string]any{"path": "."}, wtTC); err == nil {
		if found := parseFindResponse(t, findOut); len(found.Results) != 0 {
			t.Fatalf("find on whole-denied wtDir exposed entries: %+v", found.Results)
		}
	}
	if listOut, err := listing.Run(t.Context(), map[string]any{"path": ".", "max_depth": 1, "include_hidden": true}, wtTC); err == nil {
		if listed := decodeListDirOut(t, listOut); len(listed.Entries) != 0 {
			t.Fatalf("list_dir on whole-denied wtDir exposed entries: %+v", listed.Entries)
		}
	}
	if grepOut, err := grepper.Run(t.Context(), map[string]any{"path": ".", "pattern": "wt-public-needle"}, wtTC); err == nil {
		if matches := nativefixture.GrepMatches(t, grepOut); len(matches) != 0 {
			t.Fatalf("grep on whole-denied wtDir exposed matches: %+v", matches)
		}
	}
	for _, target := range []string{"public.go", "private/secret.go"} {
		if out, err := reader.Run(t.Context(), map[string]any{"path": target}, wtTC); err == nil {
			t.Fatalf("read(%q) on whole-denied wtDir unexpectedly succeeded: %s", target, out)
		}
		if out, err := wcTool.Run(t.Context(), map[string]any{"paths": []any{target}}, wtTC); err == nil {
			t.Fatalf("wc(%q) on whole-denied wtDir unexpectedly succeeded: %s", target, out)
		}
		if out, err := statTool.Run(t.Context(), map[string]any{"paths": []any{target}}, wtTC); err == nil {
			t.Fatalf("stat(%q) on whole-denied wtDir unexpectedly succeeded: %s", target, out)
		}
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

func TestReadToolExactApprovedNativeReadsAndProtectedFloor(t *testing.T) {
	previousKeyMaterial := confine.KeyMaterialWritePaths()
	confine.SetKeyMaterialPathsSource(func() []string {
		return []string{"~/.ssh/", "~/.gnupg/", "~/.age/"}
	})
	t.Cleanup(func() {
		if len(previousKeyMaterial) == 0 {
			confine.SetKeyMaterialPathsSource(nil)
			return
		}
		confine.SetKeyMaterialPathsSource(func() []string { return previousKeyMaterial })
	})
	home := t.TempDir()
	cfgDir := t.TempDir()
	proj := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "")

	sshDir := filepath.Join(home, ".ssh")
	testutil.FailErr(t, "mkdir .ssh", os.MkdirAll(sshDir, 0o700))
	idEd25519 := filepath.Join(sshDir, "id_ed25519")
	idEd25519Pub := filepath.Join(sshDir, "id_ed25519.pub")
	idRSA := filepath.Join(sshDir, "id_rsa")
	readme := filepath.Join(home, "README.md")
	testutil.FailErr(t, "write id_ed25519", os.WriteFile(idEd25519, []byte("ssh-ed25519-private-needle\n"), 0o600))
	testutil.FailErr(t, "write id_ed25519.pub", os.WriteFile(idEd25519Pub, []byte("ssh-ed25519-public-needle\n"), 0o600))
	testutil.FailErr(t, "write id_rsa", os.WriteFile(idRSA, []byte("ssh-rsa-private-needle\n"), 0o600))
	testutil.FailErr(t, "write README.md", os.WriteFile(readme, []byte("# home readme\n"), 0o600))

	apiToken := filepath.Join(cfgDir, "api.token")
	settingsYAML := filepath.Join(cfgDir, "settings.yaml")
	testutil.FailErr(t, "write api.token", os.WriteFile(apiToken, []byte("control-plane-token-needle\n"), 0o600))
	testutil.FailErr(t, "write settings.yaml", os.WriteFile(settingsYAML, []byte("model: control-plane-needle\n"), 0o600))

	dirs, err := scratch.New(cfgDir).Ensure("sess-1")
	testutil.FailErr(t, "ensure sess-1 scratch", err)
	otherDirs, err := scratch.New(cfgDir).Ensure("other-sess")
	testutil.FailErr(t, "ensure other-sess scratch", err)
	otherSecret := filepath.Join(otherDirs.Scratch, "secret.txt")
	testutil.FailErr(t, "write other-sess secret", os.WriteFile(otherSecret, []byte("other-scratch-secret\n"), 0o600))

	baseTC := func(root string) tools.ToolContext {
		tc := nativefixture.Context(root)
		tc.Host.SessionScratchDir = dirs.Scratch
		tc.Host.SessionOutputDir = dirs.Output
		return tc
	}
	homeTC := baseTC(home)
	projTC := baseTC(proj)
	cfgTC := baseTC(cfgDir)

	catalog := sourcecatalog.New()
	homeCatRoot := sourcecatalog.Root{ID: homeTC.Source.Roots[0].ID, Path: home}
	_, err = catalog.Observe(t.Context(), homeTC.Identity.ProjectID, []sourcecatalog.Root{homeCatRoot})
	testutil.FailErr(t, "warm human catalog on home", err)
	_, err = catalog.Directories.ObserveDirectory(t.Context(), homeTC.Identity.ProjectID, homeCatRoot, ".", sourcecatalog.DirectoryRead{})
	testutil.FailErr(t, "warm human directory index on home", err)

	boundary := nativefixture.Boundary(t)
	reader := &ReadTool{Boundary: boundary, Catalog: catalog}
	wcTool := &WcTool{Boundary: boundary, Catalog: catalog}
	statTool := &StatTool{Boundary: boundary, Catalog: catalog}
	finder := &FindTool{Boundary: boundary, Catalog: catalog}
	listing := &ListDirTool{Boundary: boundary, Catalog: catalog}
	grepper := &GrepTool{Boundary: boundary, Catalog: catalog}

	expectDenied := func(t *testing.T, desc string, tc tools.ToolContext, target string) {
		t.Helper()
		if out, err := reader.Run(t.Context(), map[string]any{"path": target}, tc); err == nil {
			t.Fatalf("%s: read(%q) unexpectedly succeeded: %s", desc, target, out)
		}
		if out, err := wcTool.Run(t.Context(), map[string]any{"paths": []any{target}}, tc); err == nil {
			t.Fatalf("%s: wc(%q) unexpectedly succeeded: %s", desc, target, out)
		}
		if out, err := statTool.Run(t.Context(), map[string]any{"paths": []any{target}}, tc); err == nil {
			t.Fatalf("%s: stat(%q) unexpectedly succeeded: %s", desc, target, out)
		}
	}

	expectAllowed := func(t *testing.T, desc string, tc tools.ToolContext, target, wantSubstr string) {
		t.Helper()
		out, err := reader.Run(t.Context(), map[string]any{"path": target}, tc)
		if err != nil {
			t.Fatalf("%s: read(%q) failed: %v", desc, target, err)
		}
		if !strings.Contains(out, wantSubstr) {
			t.Fatalf("%s: read(%q) = %s, missing %q", desc, target, out, wantSubstr)
		}
		if _, err := wcTool.Run(t.Context(), map[string]any{"paths": []any{target}}, tc); err != nil {
			t.Fatalf("%s: wc(%q) failed: %v", desc, target, err)
		}
		if _, err := statTool.Run(t.Context(), map[string]any{"paths": []any{target}}, tc); err != nil {
			t.Fatalf("%s: stat(%q) failed: %v", desc, target, err)
		}
	}

	// 1. Unapproved reads refused (both in attached home root and external from proj root).
	expectDenied(t, "unapproved relative in home", homeTC, ".ssh/id_ed25519")
	expectDenied(t, "unapproved abs in home", homeTC, idEd25519)
	expectDenied(t, "unapproved external from proj", projTC, idEd25519)

	// 2. Exact one-shot read approval (ApprovedFileAccess with Write: false, Tree: false) succeeds.
	oneShotHome := homeTC
	oneShotHome.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: idEd25519, Write: false}}
	expectAllowed(t, "one-shot relative in home", oneShotHome, ".ssh/id_ed25519", "ssh-ed25519-private-needle")
	expectAllowed(t, "one-shot abs in home", oneShotHome, idEd25519, "ssh-ed25519-private-needle")

	oneShotProj := projTC
	oneShotProj.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: idEd25519, Write: false}}
	expectAllowed(t, "one-shot external from proj", oneShotProj, idEd25519, "ssh-ed25519-private-needle")

	// 3a. Exact session read approval via SessionReadPaths succeeds.
	sessHome := homeTC
	sessHome.Files.SessionReadPaths = []string{idEd25519}
	expectAllowed(t, "session read path relative in home", sessHome, ".ssh/id_ed25519", "ssh-ed25519-private-needle")
	expectAllowed(t, "session read path abs in home", sessHome, idEd25519, "ssh-ed25519-private-needle")

	sessProj := projTC
	sessProj.Files.SessionReadPaths = []string{idEd25519}
	expectAllowed(t, "session read path external from proj", sessProj, idEd25519, "ssh-ed25519-private-needle")

	// 3b. Exact session read approval via SetGrantedAccessSource succeeds, and revocation refuses again.
	canonEd25519 := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(idEd25519)))
	release := projectpaths.SetGrantedAccessSource(func(_, _, abs string, write bool) (projectpaths.Access, bool) {
		if !write && abs == canonEd25519 {
			return projectpaths.Access{Path: canonEd25519, Tree: false}, true
		}
		return projectpaths.Access{}, false
	})
	expectAllowed(t, "granted access source relative in home", homeTC, ".ssh/id_ed25519", "ssh-ed25519-private-needle")
	expectAllowed(t, "granted access source external from proj", projTC, idEd25519, "ssh-ed25519-private-needle")
	release()

	// 8. Revoked grant refused again.
	expectDenied(t, "revoked granted access source in home", homeTC, ".ssh/id_ed25519")
	expectDenied(t, "revoked granted access source from proj", projTC, idEd25519)

	// 4. Sibling files and whole-tree discovery (list_dir, find, grep, recursive wc, directory stat)
	// remain denied even while id_ed25519 is approved!
	approvedTC := homeTC
	approvedTC.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: idEd25519, Write: false}}
	approvedTC.Files.SessionReadPaths = []string{idEd25519}
	for _, sibling := range []string{".ssh/id_rsa", idRSA, ".ssh/id_ed25519.pub", idEd25519Pub} {
		expectDenied(t, "sibling denied while id_ed25519 approved", approvedTC, sibling)
	}
	for _, dirTarget := range []string{".ssh", sshDir} {
		expectDenied(t, "directory read/wc/stat denied while id_ed25519 approved", approvedTC, dirTarget)
		if _, err := listing.Run(t.Context(), map[string]any{"path": dirTarget, "max_depth": 1, "include_hidden": true}, approvedTC); err == nil {
			t.Fatalf("list_dir(%q) unexpectedly succeeded while only id_ed25519 was approved", dirTarget)
		}
		if _, err := finder.Run(t.Context(), map[string]any{"path": dirTarget, "include_hidden": true}, approvedTC); err == nil {
			t.Fatalf("find(%q) unexpectedly succeeded while only id_ed25519 was approved", dirTarget)
		}
		if _, err := grepper.Run(t.Context(), map[string]any{"path": dirTarget, "pattern": "needle", "include_hidden": true}, approvedTC); err == nil {
			t.Fatalf("grep(%q) unexpectedly succeeded while only id_ed25519 was approved", dirTarget)
		}
	}
	findOut, err := finder.Run(t.Context(), map[string]any{"path": ".", "include_hidden": true}, approvedTC)
	testutil.FailErr(t, "find(.) on homeTC with id_ed25519 approved", err)
	if found := parseFindResponse(t, findOut); len(found.Results) != 1 || found.Results[0].Path != "README.md" {
		t.Fatalf("find(.) on homeTC with id_ed25519 approved = %+v, want [README.md]", found.Results)
	}
	listOut, err := listing.Run(t.Context(), map[string]any{"path": ".", "max_depth": 2, "include_hidden": true}, approvedTC)
	testutil.FailErr(t, "list_dir(.) on homeTC with id_ed25519 approved", err)
	if listed := decodeListDirOut(t, listOut); len(listed.Entries) != 1 || listed.Entries[0].Name != "README.md" {
		t.Fatalf("list_dir(.) on homeTC with id_ed25519 approved = %+v, want [README.md]", listed.Entries)
	}
	grepOut, err := grepper.Run(t.Context(), map[string]any{"path": ".", "pattern": "needle", "include_hidden": true}, approvedTC)
	testutil.FailErr(t, "grep(.) on homeTC with id_ed25519 approved", err)
	if matches := nativefixture.GrepMatches(t, grepOut); len(matches) != 0 {
		t.Fatalf("grep(.) on homeTC with id_ed25519 approved leaked key-material matches: %+v", matches)
	}

	// 5. Wrong-target grant refused.
	wrongGrantTC := homeTC
	wrongGrantTC.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: idRSA, Write: false}}
	wrongGrantTC.Files.SessionReadPaths = []string{idRSA}
	expectDenied(t, "wrong-target grant in home", wrongGrantTC, ".ssh/id_ed25519")
	expectDenied(t, "wrong-target grant abs in home", wrongGrantTC, idEd25519)

	// 6. Write-only grant refused.
	writeOnlyHome := homeTC
	writeOnlyHome.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: idEd25519, Write: true}}
	expectDenied(t, "write-only grant in home", writeOnlyHome, ".ssh/id_ed25519")
	expectDenied(t, "write-only grant abs in home", writeOnlyHome, idEd25519)

	writeOnlyProj := projTC
	writeOnlyProj.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: idEd25519, Write: true}}
	expectDenied(t, "write-only grant external from proj", writeOnlyProj, idEd25519)

	// 7. Tree grant on protected directory or file refused.
	canonSSHDir := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(sshDir)))
	treeGrantHome := homeTC
	treeGrantHome.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{
		{Path: sshDir, Write: false, Tree: true},
		{Path: idEd25519, Write: false, Tree: true},
	}
	treeGrantHome.Files.SessionReadPaths = []string{sshDir}
	releaseTree := projectpaths.SetGrantedAccessSource(func(_, _, _ string, _ bool) (projectpaths.Access, bool) {
		return projectpaths.Access{Path: canonSSHDir, Tree: true}, true
	})
	expectDenied(t, "tree grant in home (.ssh/id_ed25519)", treeGrantHome, ".ssh/id_ed25519")
	expectDenied(t, "tree grant in home (abs id_ed25519)", treeGrantHome, idEd25519)
	expectDenied(t, "tree grant in home (.ssh dir)", treeGrantHome, ".ssh")
	expectDenied(t, "tree grant external from proj", projTC, idEd25519)
	releaseTree()

	// 9. Control-plane state remains unconditionally denied with SANDBOX_CONTROL_PLANE_DENIED even with grants.
	for _, cpTarget := range []string{apiToken, settingsYAML, otherSecret} {
		canonCP := filepath.Clean(filepath.FromSlash(fspath.CanonicalPath(cpTarget)))
		releaseCP := projectpaths.SetGrantedAccessSource(func(_, _, _ string, _ bool) (projectpaths.Access, bool) {
			return projectpaths.Access{Path: canonCP, Tree: false}, true
		})
		for _, base := range []struct {
			name       string
			tc         tools.ToolContext
			allowInval bool
		}{
			{name: "projTC", tc: projTC},
			{name: "homeTC", tc: homeTC},
			{name: "cfgTC", tc: cfgTC, allowInval: true},
		} {
			tc := base.tc
			tc.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: cpTarget, Write: false}}
			tc.Files.SessionReadPaths = []string{cpTarget}
			for _, check := range []func() error{
				func() error {
					_, err := reader.Run(t.Context(), map[string]any{"path": cpTarget}, tc)
					return err
				},
				func() error {
					_, err := wcTool.Run(t.Context(), map[string]any{"paths": []any{cpTarget}}, tc)
					return err
				},
				func() error {
					_, err := statTool.Run(t.Context(), map[string]any{"paths": []any{cpTarget}}, tc)
					return err
				},
			} {
				err := check()
				var reject *toolrejection.ToolReject
				if err == nil || !errors.As(err, &reject) {
					t.Fatalf("%s: control-plane read %q with grant returned err=%v, want ToolReject", base.name, cpTarget, err)
				}
				if reject.Code != isolation.CodeControlPlaneDenied && !(base.allowInval && reject.Code == "SANDBOX_CAPABILITY_REQUEST_INVALID") {
					t.Fatalf("%s: control-plane read %q with grant returned code=%s, want %s", base.name, cpTarget, reject.Code, isolation.CodeControlPlaneDenied)
				}
			}
		}
		releaseCP()
	}
}
