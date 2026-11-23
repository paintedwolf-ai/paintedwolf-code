package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestWorktreeDirectoryReuseFourDirectoryFixtureAndMutations(t *testing.T) {
	logs := discoveryCapture(t)
	h := newHarness(t, map[string]string{
		"a.go": "package main\n",
	})
	initGitCheckout(t, h.root, nil)

	extBase := externalNonTempDir(t)
	wt := filepath.Join(extBase, "wt-discovery")
	gitCmd(t, h.root, "worktree", "add", "-q", "-b", "feat/discovery", wt)

	// Make wt contain exactly four directories: ".", ".hidden", "pkg", "src" (".git" is a regular pointer file).
	files := map[string]string{
		"src/target.go":     "source",
		".hidden/target.go": "hidden",
		"pkg/target.go":     "package",
	}
	for i := range 20 {
		files[fmt.Sprintf("src/noise-%02d.txt", i)] = "noise"
	}
	seed(t, wt, files)

	installTreeGrants(t, wt)
	catalog := discoveryCatalog(t)
	h.owners.Catalog = catalog
	h.cwd = wt

	for attempt := range 2 {
		r := h.exec("", "find", ".", "-name", "target.go")
		r.expect(t, "linked worktree find", "./.hidden/target.go\n./pkg/target.go\n./src/target.go\n", "", 0)
		work := logs.records[r.execution]
		if work["held_directory_remaining"] != 0 || work["identity_descriptor_remaining"] != 0 {
			t.Fatalf("attempt %d leaked descriptors: %v", attempt, work)
		}
		if attempt == 0 && work["directory_preparations"] != 4 {
			t.Fatalf("cold preparations = %v, want 4", work)
		}
		if attempt == 1 && (work["directory_reuses"] != 4 || work["directory_live_lists"] != 0 || work["directory_preparations"] != 0 || work["shared_root_opens"] != 1) {
			t.Fatalf("warm membership work = %v", work)
		}
	}

	// Verify zero full snapshot inventory was started in Catalog.
	status, err := catalog.Trees.IndexStatus(t.Context(), h.tc.Identity.ProjectID, sourcecatalog.Root{ID: h.tc.Source.ActiveRootID, Path: wt})
	testutil.FailErr(t, "check index status", err)
	if status.State == sourcecatalog.StateReady {
		t.Fatalf("directory lookup unexpectedly built full SQLite file index: %+v", status)
	}

	// Mutate only "src": add, rename, and remove entries; the other 3 directories must still reuse.
	testutil.FailErr(t, "add descendant", os.WriteFile(filepath.Join(wt, "src/added-target.go"), []byte("added"), 0o644))
	testutil.FailErr(t, "rename descendant", os.Rename(filepath.Join(wt, "src/target.go"), filepath.Join(wt, "src/moved.go")))
	afterMut := h.exec("", "find", ".", "-name", "*target.go")
	afterMut.expect(t, "after src mutation", "./.hidden/target.go\n./pkg/target.go\n./src/added-target.go\n", "", 0)
	mutWork := logs.records[afterMut.execution]
	if mutWork["directory_reuses"] != 3 || mutWork["directory_preparations"] != 1 {
		t.Fatalf("expected 3 reuses and 1 rebuild after single-dir mutation, got %v", mutWork)
	}
}

func TestWorktreeDirectoryLiveParityAndRevocation(t *testing.T) {
	logs := discoveryCapture(t)
	h := newHarness(t, map[string]string{
		".gitignore":        "ignored.txt\n",
		"a.txt":             "alpha\n",
		"ignored.txt":       "ignored\n",
		"sub/target.go":     "NeedleToken\n",
		".hidden/secret.go": "NeedleToken\n",
	})
	initGitCheckout(t, h.root, nil)

	extBase := externalNonTempDir(t)
	wt := filepath.Join(extBase, "wt-parity")
	gitCmd(t, h.root, "worktree", "add", "-q", "-b", "feat/parity", wt)
	seed(t, wt, map[string]string{
		"ignored.txt":       "ignored\n",
		".hidden/secret.go": "NeedleToken\n",
	})
	symlink(t, "sub", filepath.Join(wt, "link-sub"))
	symlink(t, "missing-target", filepath.Join(wt, "dangling"))

	runtime := grantedpath.NewRuntime()
	grant, err := runtime.Grant(h.tc.Identity.SessionID, grantedpath.Grant{ID: "g1", Path: wt, Mode: grantedpath.ModeRead, Tree: true})
	testutil.FailErr(t, "grant wt", err)
	t.Cleanup(projectpaths.SetGrantedAccessSource(func(session, project, path string, write bool) (projectpaths.Access, bool) {
		if write {
			return projectpaths.Access{}, false
		}
		g, ok := runtime.Covers(session, project, path, grantedpath.ModeRead)
		return projectpaths.Access{Path: g.Path, Tree: g.Tree}, ok
	}))

	catalog := discoveryCatalog(t)
	h.cwd = wt
	for _, argv := range [][]string{
		{"find", "."},
		{"find", "-L", ".", "-name", "*.go"},
		{"grep", "-rn", "NeedleToken", "."},
		{"rg", "NeedleToken"},
		{"rg", "-uu", "NeedleToken"},
		{"tree", "-a"},
	} {
		t.Run(fmt.Sprint(argv), func(t *testing.T) {
			h.owners.Catalog = nil
			live := h.exec("", argv...)
			h.owners.Catalog = catalog
			cold := h.exec("", argv...)
			warm := h.exec("", argv...)
			for name, got := range map[string]run{"cold": cold, "warm": warm} {
				if got.stdout != live.stdout || got.stderr != live.stderr || got.result != live.result || fmt.Sprint(got.err) != fmt.Sprint(live.err) {
					t.Fatalf("%s diverged from live:\ngot  %+v\nlive %+v", name, got, live)
				}
			}
		})
	}

	// Restricted ReadGlobs on an external checkout must take the live fallback without caching partial views.
	origBoundary := h.boundary
	h.boundary = sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: h.tc.ProfileID(), Tools: map[string]bool{"read": true, "find": true}, ReadGlobs: []string{".", "sub", "sub/**"},
	}})
	h.owners.Revisions.Boundary = h.boundary
	h.owners.Catalog = catalog
	restrictedRun := h.exec("", "find", "sub", "-name", "*.go")
	restrictedRun.expect(t, "restricted glob sub walk", "sub/target.go\n", "", 0)
	if logs.records[restrictedRun.execution]["directory_reuses"] != 0 || logs.records[restrictedRun.execution]["directory_preparations"] != 0 {
		t.Fatalf("restricted visibility floor should use live fallback, got %v", logs.records[restrictedRun.execution])
	}
	h.boundary = origBoundary
	h.owners.Revisions.Boundary = origBoundary

	// Revoking the grant must immediately deny access despite warm catalog entries.
	if !runtime.RevokeByID(grant.ID) {
		t.Fatal("expected grant revocation")
	}
	h.cwd = "."
	revoked := h.exec("", "find", wt, "-name", "*.go")
	revoked.refused(t, "revoked grant denied", "SURVEY_PATH_ESCAPE")

	// Cancellation releases reader pins cleanly.
	_, err = runtime.Grant(h.tc.Identity.SessionID, grantedpath.Grant{ID: "g2", Path: wt, Mode: grantedpath.ModeRead, Tree: true})
	testutil.FailErr(t, "regrant wt", err)
	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	paths, err := projectpaths.NewProgramPaths(t.Context(), h.boundary, h.tc, wt)
	testutil.FailErr(t, "bind paths", err)
	_, _ = invokeSearch(h.owners, canceledCtx, h.tc, paths, h.dispatch, requestFor(h, "find", "."), streamsFor())
	testutil.FailErr(t, "drain catalog after canceled walk", catalog.Drain(context.Background()))
}
