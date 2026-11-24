package hostcontracts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/project"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func decodeErrorDetails(t *testing.T, w *httptest.ResponseRecorder) wire.ErrorResponse {
	t.Helper()
	return contractfixture.DecodeErr(t, w)
}

func TestWorktreeLifecycleRemovalBlockersTargetedStaleCleanupAndExternalRecovery(t *testing.T) {
	ledger, database, withLedger := contractfixture.TestSourceLedger(t)
	docStore := editordoc.NewStore(database)
	gate := project.NewMutationGate()
	var docs *editordoc.Service

	srv := contractfixture.NewTestServer(t,
		withLedger,
		contractfixture.WithSessionStore(sessionstore.NewSQL(database)),
		func(d *hostapi.Dependencies) {
			d.Core.MutationGate = gate
			if d.Core.Sessions != nil {
				d.Core.Sessions.Runner.Execution.SetMutationGate(gate)
			}
			docs = editordoc.New(docStore, ledger, ledger.History, d.Core.Projects)
			d.Source.EditorDocuments = docs
		},
	)
	t.Cleanup(func() { _ = docs.Close(context.Background()) })

	baseDir := gitrepo.CanonicalDir(t.TempDir())
	primaryDir := filepath.Join(baseDir, "primary")
	liveSiblingDir := filepath.Join(baseDir, "wt-live-sibling")
	staleSiblingDir := filepath.Join(baseDir, "wt-stale-sibling")
	externalRemovedDir := filepath.Join(baseDir, "wt-external-removed")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primaryDir, 0o755))

	contractfixture.RunGitIn(t, primaryDir, "init", "-b", "main")
	testutil.FailErr(t, "seed app.go", os.WriteFile(filepath.Join(primaryDir, "app.go"), []byte("package main\n"), 0o644))
	contractfixture.RunGitIn(t, primaryDir, "add", "app.go")
	contractfixture.RunGitIn(t, primaryDir, "commit", "-m", "initial")

	// Create one live external sibling worktree and one separate stale external sibling registration.
	contractfixture.RunGitIn(t, primaryDir, "worktree", "add", "-b", "feat/live-sibling", liveSiblingDir, "HEAD")
	contractfixture.RunGitIn(t, primaryDir, "worktree", "add", "-b", "feat/stale-sibling", staleSiblingDir, "HEAD")
	contractfixture.RunGitIn(t, primaryDir, "worktree", "add", "-b", "feat/ext-recovery", externalRemovedDir, "HEAD")
	testutil.FailErr(t, "delete stale sibling dir to leave stale registration", os.RemoveAll(staleSiblingDir))

	repoDiscovered, ok := gitrepo.Discover(primaryDir)
	if !ok {
		t.Fatal("discover primary repo")
	}
	staleAdminDir := filepath.Join(repoDiscovered.CommonDir, "worktrees", "wt-stale-sibling")
	if _, err := os.Stat(filepath.Join(staleAdminDir, "gitdir")); err != nil {
		t.Fatalf("expected stale sibling admin dir before test: %v", err)
	}

	proj, err := srv.Sources.Workspace.ProjectRegistry.Create(t.Context(), project.CreateParams{
		Roots: []project.AttachRootParams{{Path: primaryDir, Label: "repo"}},
	})
	testutil.FailErr(t, "create project", err)
	rootID := proj.Roots[0].ID

	sess := contractfixture.CreateSessionForProject(t, srv, proj.ID)
	repoID := contractfixture.WorktreeRepoID(t, srv, proj.ID)

	bw, bindView := contractfixture.BindWorktree(t, srv, sess.ID, repoID, "session/managed-a")
	if bw.Code != http.StatusOK || !bindView.Bound || bindView.WorktreeID == "" {
		t.Fatalf("bind worktree = %d view=%+v body=%s", bw.Code, bindView, bw.Body.String())
	}

	binding, bound, err := srv.Admin.SessionAdmin.Lifecycle.SessionStore.GetWorktreeBinding(t.Context(), sess.ID)
	testutil.FailErr(t, "get binding", err)
	if !bound {
		t.Fatal("expected bound worktree")
	}

	// Verify managed=true on bound checkout and managed=false on external checkouts in GET /worktrees.
	wtReq := contractfixture.NewAuthedRequest(http.MethodGet, "/v1/projects/"+proj.ID+"/git/repos/"+repoID+"/worktrees?session_id="+sess.ID, nil)
	wtRes := httptest.NewRecorder()
	srv.ServeHTTP(wtRes, wtReq)
	if wtRes.Code != http.StatusOK {
		t.Fatalf("list worktrees status=%d body=%s", wtRes.Code, wtRes.Body.String())
	}
	var wtList wire.GitWorktreesView
	testutil.FailErr(t, "decode worktrees", json.Unmarshal(wtRes.Body.Bytes(), &wtList))
	var externalRecoveryID string
	for _, entry := range wtList.Worktrees {
		if entry.WorktreeID == bindView.WorktreeID && !entry.Managed {
			t.Fatalf("expected bound checkout %s to be marked managed=true: %+v", bindView.WorktreeID, entry)
		}
		if entry.Branch == "feat/ext-recovery" {
			externalRecoveryID = entry.WorktreeID
			if entry.Managed {
				t.Fatalf("external checkout must not be marked managed: %+v", entry)
			}
		}
	}
	if externalRecoveryID == "" {
		t.Fatalf("missing external recovery checkout in %+v", wtList.Worktrees)
	}

	// Blocker 1: Dirty working tree refuses removal with concrete dirty_worktree blocker details.
	testutil.FailErr(t, "dirty managed checkout", os.WriteFile(filepath.Join(binding.WorktreePath, "app.go"), []byte("package main // dirty\n"), 0o644))
	resDirty := contractfixture.RemoveWorktree(t, srv, sess.ID)
	if resDirty.Code != http.StatusConflict {
		t.Fatalf("dirty remove status=%d want 409 body=%s", resDirty.Code, resDirty.Body.String())
	}
	errDirty := decodeErrorDetails(t, resDirty)
	if errDirty.Details["blocker"] != "dirty_worktree" || errDirty.Details["path"] != "app.go" || errDirty.Details["root_id"] != rootID {
		t.Fatalf("unexpected dirty blocker details: %+v", errDirty)
	}
	// Resolve dirty file by restoring clean content.
	testutil.FailErr(t, "clean managed checkout", os.WriteFile(filepath.Join(binding.WorktreePath, "app.go"), []byte("package main\n"), 0o644))

	// Blocker 2: Child worker session actively running refuses removal with active_work blocker.
	workerSessID := "worker-" + uuid.NewString()
	testdbseed.InsertChildSession(t, database, workerSessID, sess.ID)
	unlockWorker, ok := srv.Admin.SessionAdmin.Lifecycle.Sessions.Runner.Execution.TryIdleMutation(workerSessID)
	if !ok {
		t.Fatal("lock worker session")
	}
	resActive := contractfixture.RemoveWorktree(t, srv, sess.ID)
	if resActive.Code != http.StatusConflict {
		unlockWorker()
		t.Fatalf("active worker remove status=%d want 409 body=%s", resActive.Code, resActive.Body.String())
	}
	errActive := decodeErrorDetails(t, resActive)
	if errActive.Details["blocker"] != "active_work" || errActive.Details["actor_session_id"] != workerSessID {
		unlockWorker()
		t.Fatalf("unexpected active_work blocker details: %+v", errActive)
	}
	unlockWorker()

	// Blocker 3: Unsaved editor draft in another window refuses removal with unsaved_editor blocker.
	projectedProj, _, _, _, resolved := srv.Admin.Git.ResolveCheckoutProject(t.Context(), proj, bindView.WorktreeID)
	if !resolved {
		t.Fatal("resolve managed checkout project")
	}
	docOtherWin, err := docs.Open(t.Context(), projectedProj, "app.go", rootID, "", "window-other", nil)
	testutil.FailErr(t, "open doc in window-other", err)
	docOtherWin, err = docs.ReplaceSnapshot(t.Context(), docOtherWin.ID, proj.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "window-other",
			OperationID:      uuid.NewString(),
			ExpectedRevision: docOtherWin.Revision,
		},
		Content: "package main // unsaved in window-other\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "edit doc in window-other", err)

	resUnsaved := contractfixture.RemoveWorktree(t, srv, sess.ID)
	if resUnsaved.Code != http.StatusConflict {
		t.Fatalf("unsaved editor remove status=%d want 409 body=%s", resUnsaved.Code, resUnsaved.Body.String())
	}
	errUnsaved := decodeErrorDetails(t, resUnsaved)
	if errUnsaved.Details["blocker"] != "unsaved_editor" || errUnsaved.Details["document_id"] != docOtherWin.ID || errUnsaved.Details["path"] != "app.go" {
		t.Fatalf("unexpected unsaved_editor blocker details: %+v", errUnsaved)
	}

	// Discard the unsaved draft in window-other to clear Blocker 3.
	_, err = docs.Discard(t.Context(), docOtherWin.ID, proj.ID, editordoc.DocumentCommand{
		ClientID:         "window-other",
		OperationID:      uuid.NewString(),
		ExpectedRevision: docOtherWin.Revision,
	})
	testutil.FailErr(t, "discard window-other draft", err)

	// Blocker 4: In-flight project save/mutation gate refuses removal until drained.
	testutil.FailErr(t, "begin concurrent save gate", gate.BeginMutation(proj.ID))
	resSaving := contractfixture.RemoveWorktree(t, srv, sess.ID)
	gate.EndMutation(proj.ID)
	if resSaving.Code != http.StatusConflict {
		t.Fatalf("in-flight save remove status=%d want 409 body=%s", resSaving.Code, resSaving.Body.String())
	}
	errSaving := decodeErrorDetails(t, resSaving)
	if errSaving.Details["blocker"] != "pending_save" {
		t.Fatalf("unexpected pending_save blocker details: %+v", errSaving)
	}

	// All blockers resolved: removal succeeds and leaves BOTH live sibling AND stale sibling registrations untouched!
	resOK := contractfixture.RemoveWorktree(t, srv, sess.ID)
	if resOK.Code != http.StatusNoContent {
		t.Fatalf("clean remove status=%d body=%s", resOK.Code, resOK.Body.String())
	}
	if _, err := os.Stat(binding.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected managed worktree directory to be removed, err=%v", err)
	}
	if _, err := os.Stat(liveSiblingDir); err != nil {
		t.Fatalf("live sibling worktree was disturbed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staleAdminDir, "gitdir")); err != nil {
		t.Fatalf("unrelated stale sibling registration was pruned by single-worktree removal: %v", err)
	}

	// Also test stale managed cleanup when the managed worktree directory was deleted externally:
	// only the managed registration is removed, while staleAdminDir STILL survives!
	sess2 := contractfixture.CreateSessionForProject(t, srv, proj.ID)
	bw2, bindView2 := contractfixture.BindWorktree(t, srv, sess2.ID, repoID, "session/managed-missing")
	if bw2.Code != http.StatusOK || !bindView2.Bound {
		t.Fatalf("bind sess2 = %d body=%s", bw2.Code, bw2.Body.String())
	}
	binding2, _, err := srv.Admin.SessionAdmin.Lifecycle.SessionStore.GetWorktreeBinding(t.Context(), sess2.ID)
	testutil.FailErr(t, "get binding2", err)
	testutil.FailErr(t, "remove managed2 dir externally", os.RemoveAll(binding2.WorktreePath))

	resMissingCleanup := contractfixture.RemoveWorktree(t, srv, sess2.ID)
	if resMissingCleanup.Code != http.StatusNoContent {
		t.Fatalf("missing managed cleanup status=%d body=%s", resMissingCleanup.Code, resMissingCleanup.Body.String())
	}
	if _, err := os.Stat(filepath.Join(staleAdminDir, "gitdir")); err != nil {
		t.Fatalf("unrelated stale sibling registration was pruned during missing-managed cleanup: %v", err)
	}

	// Verify native/external removal leaves editor draft recovery usable via OpenByID!
	extProjected, _, _, _, okExt := srv.Admin.Git.ResolveCheckoutProject(t.Context(), proj, externalRecoveryID)
	if !okExt {
		t.Fatal("resolve external checkout project")
	}
	extDoc, err := docs.Open(t.Context(), extProjected, "app.go", rootID, "", "window-ext", nil)
	testutil.FailErr(t, "open external doc", err)
	_, err = docs.ReplaceSnapshot(t.Context(), extDoc.ID, proj.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{
			ClientID:         "window-ext",
			OperationID:      uuid.NewString(),
			ExpectedRevision: extDoc.Revision,
		},
		Content: "package main // recoverable external draft\n",
		EOL:     "lf",
	})
	testutil.FailErr(t, "edit external doc", err)

	// Externally remove externalRemovedDir via native git worktree remove --force.
	contractfixture.RunGitIn(t, primaryDir, "worktree", "remove", "--force", externalRemovedDir)

	recovered, err := docs.OpenByID(t.Context(), proj.ID, extDoc.ID, "", "window-ext-reopen", nil)
	testutil.FailErr(t, "reopen orphaned external draft by ID", err)
	if !recovered.Dirty || recovered.Draft != "package main // recoverable external draft\n" {
		t.Fatalf("recovered external draft lost content: %+v", recovered)
	}
}
