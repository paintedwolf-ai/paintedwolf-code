package survey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestScratchSurveySeesWritesAfterEmptyInventory(t *testing.T) {
	root, scratch := t.TempDir(), t.TempDir()
	tc := nativefixture.Context(root)
	tc.Host.SessionScratchDir = scratch
	catalog := sourcecatalog.New()
	obsCtx := sandbox.WithSessionDirs(t.Context(), scratch, tc.Host.SessionOutputDir)
	_, err := catalog.Observe(obsCtx, tc.Identity.ProjectID, []sourcecatalog.Root{{ID: projectroot.VirtualScratchLabel, Path: scratch}})
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

	tc := nativefixture.Context(cfgDir)
	tc.Host.SessionScratchDir = ownScratch
	tc.Host.SessionOutputDir = ownOutput
	primaryRoot := tc.Source.Roots[0]

	// Pre-populate warmCatalog before activating LYCAON_CONFIG_DIR so the snapshot
	// holds indexed entries for api.token, settings.yaml, store.db, and scratch/other,
	// proving that listRootCatalog, list_dir, find, and grep still deny control-plane
	// state even against an already-populated snapshot.
	warmCatalog := sourcecatalog.New()
	warmSnap, err := warmCatalog.Observe(t.Context(), tc.Identity.ProjectID, []sourcecatalog.Root{{ID: primaryRoot.ID, Path: cfgDir}})
	testutil.FailErr(t, "pre-populate warm catalog", err)
	if children, ok := warmSnap.Listing(primaryRoot.ID, "."); !ok || len(children) == 0 {
		t.Fatalf("pre-populated warm catalog did not index root: ok=%v children=%d", ok, len(children))
	}

	t.Setenv(configdir.EnvConfigDir, cfgDir)

	// Observing a fresh catalog after LYCAON_CONFIG_DIR is active prunes control-plane entries at SurveyWalk.
	liveCatalog := sourcecatalog.New()
	liveSnap, err := liveCatalog.Observe(t.Context(), tc.Identity.ProjectID, []sourcecatalog.Root{{ID: primaryRoot.ID, Path: cfgDir}})
	testutil.FailErr(t, "observe live catalog on control-plane root", err)
	if children, _ := liveSnap.Listing(primaryRoot.ID, "."); len(children) != 0 {
		t.Fatalf("live catalog indexed control-plane root entries: %+v, want 0", children)
	}

	boundary := nativefixture.Boundary(t)
	sessionCtx := sandbox.WithSessionDirs(t.Context(), ownScratch, ownOutput)

	// 1. Direct listRootCatalog against the pre-populated warmCatalog must refuse control-plane root and other-session scratch.
	listing := &ListDirTool{Boundary: boundary, Catalog: warmCatalog}
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

	// 3. Agent find and grep on "." when configdir.Root() is the sole root refuse with SANDBOX_CONTROL_PLANE_DENIED,
	// and direct runFindRoot against the pre-populated warmCatalog collects 0 control-plane entries.
	finder := &FindTool{Boundary: boundary, Catalog: warmCatalog}
	if _, findErr := finder.Run(t.Context(), map[string]any{"path": ".", "name_glob": "*"}, tc); !isSurveyControlPlaneReject(findErr) {
		t.Fatalf("find(.) on sole attached configdir.Root() err = %v, want %s", findErr, isolation.CodeControlPlaneDenied)
	}
	var directResp findResponse
	var skipped, matchedTotal int
	var truncated, deeperOmitted bool
	testutil.FailErr(t, "direct runFindRoot on warmCatalog", finder.runFindRoot(
		sessionCtx, tc, primaryRoot, cfgDir, ".", 0, 100, 0, "*", findTypeAny, true, nil,
		&directResp, &skipped, &matchedTotal, &truncated, &deeperOmitted,
	))
	if len(directResp.Results) != 0 || matchedTotal != 0 {
		t.Fatalf("direct runFindRoot on warmCatalog leaked results: %+v (matchedTotal=%d)", directResp.Results, matchedTotal)
	}
	grepper := &GrepTool{Boundary: boundary, Catalog: warmCatalog}
	if _, grepErr := grepper.Run(t.Context(), map[string]any{"path": ".", "pattern": "needle"}, tc); !isSurveyControlPlaneReject(grepErr) {
		t.Fatalf("grep(.) on sole attached configdir.Root() err = %v, want %s", grepErr, isolation.CodeControlPlaneDenied)
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

	// 5. Directly attaching another session's scratch directory (<config>/scratch/other)
	// as the project root cannot confer read or survey authority when the invocation
	// owns "own" (or has no session context).
	otherRootTC := nativefixture.Context(otherScratch)
	otherRootTC.Host.SessionScratchDir = ownScratch
	otherRootTC.Host.SessionOutputDir = ownOutput
	otherPrimary := otherRootTC.Source.Roots[0]
	if entries, ok := listing.listRootCatalog(sessionCtx, otherRootTC, otherPrimary, otherScratch, 1, true); ok || len(entries) != 0 {
		t.Fatalf("listRootCatalog on directly attached other-session scratch = (%+v, %v), want (nil, false)", entries, ok)
	}
	if _, listOtherErr := listing.Run(t.Context(), map[string]any{"path": ".", "include_hidden": true}, otherRootTC); !isSurveyControlPlaneReject(listOtherErr) {
		t.Fatalf("list_dir(.) on directly attached other-session scratch err = %v, want %s", listOtherErr, isolation.CodeControlPlaneDenied)
	}
	if _, findOtherErr := finder.Run(t.Context(), map[string]any{"path": ".", "name_glob": "*"}, otherRootTC); !isSurveyControlPlaneReject(findOtherErr) {
		t.Fatalf("find(.) on directly attached other-session scratch err = %v, want %s", findOtherErr, isolation.CodeControlPlaneDenied)
	}
	if _, grepOtherErr := grepper.Run(t.Context(), map[string]any{"path": ".", "pattern": "needle"}, otherRootTC); !isSurveyControlPlaneReject(grepOtherErr) {
		t.Fatalf("grep(.) on directly attached other-session scratch err = %v, want %s", grepOtherErr, isolation.CodeControlPlaneDenied)
	}
}

func isSurveyControlPlaneReject(err error) bool {
	var reject *toolrejection.ToolReject
	return errors.As(err, &reject) && reject.Code == isolation.CodeControlPlaneDenied
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
