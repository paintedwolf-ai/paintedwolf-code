package commands

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type instrumentedGitLister struct {
	mgr             *git.Manager
	listCalls       atomic.Int64
	statusCalls     atomic.Int64
	nonPrimaryCalls atomic.Int64
	primaryToplevel string
}

func (l *instrumentedGitLister) ListWorktrees(ctx context.Context, toplevel string) ([]git.WorktreeEntry, error) {
	l.listCalls.Add(1)
	if gitrepo.CanonicalDir(toplevel) != l.primaryToplevel {
		l.nonPrimaryCalls.Add(1)
	}
	return l.mgr.ListWorktrees(ctx, toplevel)
}

func TestWorktreeQualificationRapidEventsWorkCountGate(t *testing.T) {
	h := wiring.BuildForTest(t)
	baseDir := gitrepo.CanonicalDir(t.TempDir())
	primaryDir := filepath.Join(baseDir, "primary")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(filepath.Join(primaryDir, "src", "nested"), 0o755))
	testutil.FailErr(t, "seed nested file", os.WriteFile(filepath.Join(primaryDir, "src", "nested", "mod.go"), []byte("package nested\n"), 0o644))
	gittest.InitCommit(t, primaryDir, "seed")

	const siblingCount = 12
	for i := range siblingCount {
		dir := filepath.Join(baseDir, fmt.Sprintf("wt-idle-%02d", i))
		gittest.Run(t, primaryDir, "worktree", "add", "-b", fmt.Sprintf("feat/idle-%02d", i), dir, "HEAD")
		testutil.FailErr(t, "dirty idle sibling", os.WriteFile(filepath.Join(dir, "src", "nested", "mod.go"), []byte(fmt.Sprintf("package nested // %d\n", i)), 0o644))
	}

	proj, err := h.Server.Sources.Workspace.ProjectRegistry.Create(t.Context(), project.CreateParams{
		Roots: []project.AttachRootParams{{Path: primaryDir, Label: "repo"}},
	})
	testutil.FailErr(t, "create project", err)
	repos := git.DiscoverRepos(t.Context(), []projectroot.RootRef{{ID: proj.Roots[0].ID, Label: "repo", Path: primaryDir, IsPrimary: true}})
	if len(repos) != 1 {
		t.Fatalf("repos = %+v", repos)
	}
	spec := worktrees.RepoSpec{ProjectID: proj.ID, RepoID: repos[0].ID, Toplevel: primaryDir}

	lister := &instrumentedGitLister{
		mgr:             git.NewManager(),
		primaryToplevel: gitrepo.CanonicalDir(primaryDir),
	}
	var inspectorCalls atomic.Int64
	inv := worktrees.New(h.DB, worktrees.Options{
		Git: lister,
		Inspector: func(commonDir, listedPath string) gitrepo.RegisteredCheckout {
			inspectorCalls.Add(1)
			reg, _ := gitrepo.VerifyRegisteredCheckout(commonDir, listedPath)
			return reg
		},
	})

	// Fire 50 rapid duplicate change notifications concurrently across the 12 idle siblings.
	const duplicateEvents = 50
	var wg sync.WaitGroup
	for range duplicateEvents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inv.ScheduleRefresh(t.Context(), spec)
		}()
	}
	wg.Wait()

	items, err := inv.Read(t.Context(), spec)
	testutil.FailErr(t, "read coalesced inventory", err)
	if len(items) != siblingCount+1 {
		t.Fatalf("inventory items = %d, want %d", len(items), siblingCount+1)
	}

	// Wait briefly for any trailing background pass already admitted before Read returned.
	testutil.WaitFor(t, "inspector settles", func() bool {
		return inspectorCalls.Load() == int64(siblingCount+1)*lister.listCalls.Load()
	})

	listCalls := lister.listCalls.Load()
	if listCalls < 1 || listCalls > 2 {
		t.Fatalf("listCalls = %d across %d rapid events, want 1..2 coalesced builds", listCalls, duplicateEvents)
	}
	if got := lister.statusCalls.Load(); got != 0 {
		t.Fatalf("eager sibling git status calls = %d, want 0", got)
	}
	if got := lister.nonPrimaryCalls.Load(); got != 0 {
		t.Fatalf("per-descendant or sibling git launches = %d, want 0", got)
	}
	t.Logf("work-count gate: siblings=%d events=%d list_builds=%d status_reads=%d non_primary_git=%d inspector_checks=%d",
		siblingCount, duplicateEvents, listCalls, lister.statusCalls.Load(), lister.nonPrimaryCalls.Load(), inspectorCalls.Load())
}

func materializeUpgradeCorpusData(t *testing.T, corpusDir, targetDataDir, dbFileName string) {
	t.Helper()
	archive, err := zip.OpenReader(filepath.Join(corpusDir, "backup.zip"))
	testutil.FailErr(t, "open corpus backup.zip", err)
	defer func() { _ = archive.Close() }()

	readArchiveEntry := func(name string) []byte {
		f, openErr := archive.Reader.Open(name)
		testutil.FailErr(t, "open zip entry "+name, openErr)
		defer func() { _ = f.Close() }()
		data, readErr := io.ReadAll(f)
		testutil.FailErr(t, "read zip entry "+name, readErr)
		return data
	}

	var manifest struct {
		RetainedBranchTrees []string `json:"retained_branch_trees"`
		Files               []struct {
			RelPath string      `json:"rel_path"`
			Kind    string      `json:"kind"`
			Mode    fs.FileMode `json:"mode"`
		} `json:"files"`
	}
	testutil.FailErr(t, "decode backup manifest", json.Unmarshal(readArchiveEntry("manifest.json"), &manifest))

	safeJoin := func(rel string) string {
		clean := path.Clean(rel)
		if path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			t.Fatalf("unsafe corpus path %q", rel)
		}
		return filepath.Join(targetDataDir, filepath.FromSlash(clean))
	}

	for _, tree := range manifest.RetainedBranchTrees {
		testutil.FailErr(t, "mkdir retained tree", os.MkdirAll(safeJoin(tree), 0o700))
	}
	for _, item := range manifest.Files {
		dst := safeJoin(item.RelPath)
		testutil.FailErr(t, "mkdir retained file parent", os.MkdirAll(filepath.Dir(dst), 0o700))
		body := readArchiveEntry(item.RelPath)
		if item.Kind == "symlink" {
			testutil.FailErr(t, "symlink retained item", os.Symlink(string(body), dst))
		} else {
			mode := item.Mode.Perm()
			if mode == 0 {
				mode = 0o600
			}
			testutil.FailErr(t, "write retained item", os.WriteFile(dst, body, mode))
		}
	}

	rawDB, err := os.ReadFile(filepath.Join(corpusDir, "store.db"))
	testutil.FailErr(t, "read corpus store.db", err)
	testutil.FailErr(t, "write target store", os.WriteFile(filepath.Join(targetDataDir, dbFileName), rawDB, 0o600))
}

func TestWorktreeQualificationPreFeatureStoreUpgradeAndRestart(t *testing.T) {
	const (
		corpusProjectID   = "664320c2-ad53-479a-aa43-e0f19e1b49b3"
		corpusRootID      = "8b626d13-2d5d-41ca-8c78-bfda0bcf6e26"
		corpusSessionID   = "e057114f-4162-40a2-b9ee-f77ebdb92ffc"
		corpusDirtyDocID  = "6102f96e-29ff-4072-b614-04dd2f3795f1"
		corpusDirtyFileID = "f848cfe6-d8b0-4914-8260-577dfc5149cc"
		corpusCleanDocID  = "17bc47b9-954f-41cc-92c5-e833ceaa617a"
		preWorktreeID     = "00000000-0000-4000-8000-000000000099"
	)

	testDir := gitrepo.CanonicalDir(t.TempDir())
	baseDir := gitrepo.CanonicalDir(t.TempDir())
	primaryDir := filepath.Join(baseDir, "corpus-primary")
	managedDir := filepath.Join(baseDir, "corpus-managed-wt")
	outsideDir := gitrepo.CanonicalDir(outsideCommandFolder(t))
	outsideFile := filepath.Join(outsideDir, "captured-outside.txt")

	testutil.FailErr(t, "mkdir primaryDir", os.MkdirAll(primaryDir, 0o755))
	testutil.FailErr(t, "seed README.md", os.WriteFile(filepath.Join(primaryDir, "README.md"), []byte("# corpus seed\n"), 0o644))
	testutil.FailErr(t, "seed editor-fixture.txt", os.WriteFile(filepath.Join(primaryDir, "editor-fixture.txt"), []byte("editor base\nsaved window edit\n"), 0o644))
	testutil.FailErr(t, "seed upgrade-fixture-history.txt", os.WriteFile(filepath.Join(primaryDir, "upgrade-fixture-history.txt"), []byte("upgraded history\n"), 0o644))
	testutil.FailErr(t, "seed outside file", os.WriteFile(outsideFile, []byte("outside file bytes\n"), 0o600))

	gittest.InitCommit(t, primaryDir, "corpus seed")
	gittest.Run(t, primaryDir, "worktree", "add", "-b", "session/upgraded", managedDir, "HEAD")

	repos := git.DiscoverRepos(t.Context(), []projectroot.RootRef{{ID: corpusRootID, Label: "seed-project", Path: primaryDir, IsPrimary: true}})
	if len(repos) != 1 {
		t.Fatalf("discover primary repo: %+v", repos)
	}
	repoID := repos[0].ID

	corpusSrc := filepath.Join(configlayout.FindModuleRoot(), "testdata", "upgrade-corpus", "1.0.3-rc1")
	materializeUpgradeCorpusData(t, corpusSrc, testDir, "wiring-test.db")
	dbPath := filepath.Join(testDir, "wiring-test.db")

	preDB, err := sql.Open("sqlite", dbPath)
	testutil.FailErr(t, "open pre-upgrade db", err)
	_, err = preDB.ExecContext(t.Context(), `UPDATE project_roots SET path = ?, is_primary = 1 WHERE id = ?`, primaryDir, corpusRootID)
	testutil.FailErr(t, "point corpus root at primaryDir", err)
	_, err = preDB.ExecContext(t.Context(),
		`INSERT INTO source_worktrees (id, project_id, repo_id, toplevel, worktree_path, branch, base_branch, created_at) VALUES (?, ?, ?, ?, ?, 'session/upgraded', 'main', '2026-10-20T22:57:48.000000000Z')`,
		preWorktreeID, corpusProjectID, repoID, primaryDir, managedDir,
	)
	testutil.FailErr(t, "seed pre-feature source_worktree", err)
	_, err = preDB.ExecContext(t.Context(),
		`INSERT INTO session_worktrees (session_id, project_id, worktree_id, created_at) VALUES (?, ?, ?, '2026-10-20T22:57:48.000000000Z')`,
		corpusSessionID, corpusProjectID, preWorktreeID,
	)
	testutil.FailErr(t, "seed pre-feature session_worktree", err)
	testutil.FailErr(t, "close pre-upgrade db", preDB.Close())

	var capturedOutside wire.OpenedOutsideFile

	t.Run("upgrade_boot", func(t *testing.T) {
		h := wiring.BuildForTestAt(t, testDir)

		boundView := doJSON[wire.GitWorktreeView](t, h, http.MethodGet, "/v1/sessions/"+corpusSessionID+"/git-worktree", nil, http.StatusOK)
		if !boundView.Bound || boundView.WorktreeID != preWorktreeID || boundView.Branch != "session/upgraded" {
			t.Fatalf("upgraded session binding mismatch: %+v", boundView)
		}

		wtView := doJSON[wire.GitWorktreesView](t, h, http.MethodGet, "/v1/projects/"+corpusProjectID+"/git/repos/"+repoID+"/worktrees?session_id="+corpusSessionID, nil, http.StatusOK)
		foundManaged := false
		for _, item := range wtView.Worktrees {
			if item.WorktreeID == preWorktreeID {
				foundManaged = true
				if !item.Managed || !item.BoundToSession || !item.Available {
					t.Fatalf("upgraded managed worktree flags wrong: %+v", item)
				}
			}
		}
		if !foundManaged {
			t.Fatalf("missing upgraded worktree %s in %+v", preWorktreeID, wtView.Worktrees)
		}

		capturedOutside = doJSON[wire.OpenedOutsideFile](t, h, http.MethodPost, "/v1/projects/"+corpusProjectID+"/source/outside-files", wire.OpenOutsideFileRequest{
			SessionID: corpusSessionID,
			Path:      outsideFile,
		}, http.StatusOK)
		if capturedOutside.FileID == "" || capturedOutside.RootID == "" {
			t.Fatalf("captured outside file = %+v", capturedOutside)
		}
	})

	t.Run("restart_verify", func(t *testing.T) {
		h := wiring.BuildForTestAt(t, testDir)

		boundView := doJSON[wire.GitWorktreeView](t, h, http.MethodGet, "/v1/sessions/"+corpusSessionID+"/git-worktree", nil, http.StatusOK)
		if !boundView.Bound || boundView.WorktreeID != preWorktreeID {
			t.Fatalf("restarted session binding mismatch: %+v", boundView)
		}

		dirtyDoc := doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+corpusProjectID+"/editor-documents", wire.OpenEditorDocumentRequest{
			ClientID: "upgrade-verify-win",
			RootID:   corpusRootID,
			Path:     "editor-fixture.txt",
		}, http.StatusCreated)
		if dirtyDoc.ID != corpusDirtyDocID || dirtyDoc.FileID != corpusDirtyFileID || !dirtyDoc.Dirty || dirtyDoc.Revision < 4 {
			t.Fatalf("migrated dirty editor document lost identity or draft: %+v", dirtyDoc)
		}

		cleanDoc := doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+corpusProjectID+"/editor-documents", wire.OpenEditorDocumentRequest{
			ClientID: "upgrade-verify-win",
			RootID:   corpusRootID,
			Path:     "README.md",
		}, http.StatusCreated)
		if cleanDoc.ID != corpusCleanDocID || cleanDoc.Dirty {
			t.Fatalf("migrated clean editor document mismatch: %+v", cleanDoc)
		}

		outsideDoc := doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+corpusProjectID+"/editor-documents", wire.OpenEditorDocumentRequest{
			ClientID: "upgrade-verify-win",
			FileID:   capturedOutside.FileID,
		}, http.StatusCreated)
		if outsideDoc.FileID != capturedOutside.FileID || outsideDoc.RootID != capturedOutside.RootID {
			t.Fatalf("outside document after restart = %+v", outsideDoc)
		}

		siblingDoc := doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+corpusProjectID+"/editor-documents?worktree_id="+preWorktreeID, wire.OpenEditorDocumentRequest{
			ClientID: "upgrade-verify-win",
			RootID:   corpusRootID,
			Path:     "editor-fixture.txt",
		}, http.StatusCreated)
		if siblingDoc.ID == corpusDirtyDocID || siblingDoc.WorkspaceID != preWorktreeID || siblingDoc.Dirty {
			t.Fatalf("sibling checkout document collided with primary draft: %+v", siblingDoc)
		}
	})
}
