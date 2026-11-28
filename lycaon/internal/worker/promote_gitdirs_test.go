package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

type linkedWorktreePromoteFixture struct {
	primaryDir  string
	worktreeDir string
	adminDir    string
	service     *MergeService
	task        *api.WorkerTask
	jobID       string
	roots       []projectroot.RootRef
}

func setupLinkedWorktreePromoteFixture(t *testing.T, editWorker bool) linkedWorktreePromoteFixture {
	t.Helper()
	ctx := t.Context()
	base := t.TempDir()
	primaryDir := filepath.Join(base, "primary-repo")
	worktreeDir := filepath.Join(base, "wt-branch")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primaryDir, 0o755))
	testutil.FailErr(t, "seed primary file", os.WriteFile(filepath.Join(primaryDir, "app.txt"), []byte("primary-base\n"), 0o644))
	gittest.InitCommit(t, primaryDir, "initial commit")
	gittest.Run(t, primaryDir, "worktree", "add", "-b", "wt-branch", worktreeDir)
	adminDir := filepath.Join(primaryDir, ".git", "worktrees", "wt-branch")

	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, database, testdbseed.DefaultProjectID, "root-wt", worktreeDir)
	testdbseed.InsertSession(t, database, "parent", testdbseed.DefaultProjectID)
	dataDir := testbaseline.DataDir(t, database)

	selector := platform.NewSelector(platform.Config{})
	selector.BudgetFor = func(class adapter.BudgetClass) adapter.BudgetFact {
		return adapter.BudgetFact{Class: class, Limit: 1 << 20, EntryCost: 1}
	}
	selector.Confinement = nil
	manager := workspace.NewManager(filepath.Join(dataDir, "worker-branches"), selector, workspace.Filesystem{})

	queueServices := NewSQLServices(database, 4)
	queue := queueServices.Queue
	queue.Branches.SetBaselineStore(sourceledger.New(database, filepath.Join(dataDir, "source-content"), sourceledger.Timing{}).Baselines)
	queue.Branches.SetWorkerWorkspaceManager(manager)

	jobID, err := queue.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   worktreeDir,
		WorkspaceRootID: "root-wt",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"."}},
		AgentType:       "implementer",
		Prompt:          "edit in linked worktree",
		Brief:           "edit in linked worktree",
	})
	testutil.FailErr(t, "enqueue", err)

	next, err := queue.ClaimNext(ctx, ClaimRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		ClaimedBy:       "test",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim next", err)
	task, err := queue.Branches.ClaimWorkerBranch(ctx, jobID)
	testutil.FailErr(t, "claim branch", err)
	task.ClaimToken = next.ClaimToken

	if editWorker {
		testutil.FailErr(t, "worker edit app.txt", os.WriteFile(filepath.Join(task.WorkspaceRoot, "app.txt"), []byte("worker-edit\n"), 0o644))
		testutil.FailErr(t, "worker add file", os.WriteFile(filepath.Join(task.WorkspaceRoot, "worker_added.txt"), []byte("added-by-worker\n"), 0o644))
		// Record a worker commit in the isolated private Git repo so checkpoint promotion
		// imports real worker Git refs and objects into the linked worktree's shared repository.
		gittest.Run(t, task.WorkspaceRoot, "add", "app.txt", "worker_added.txt")
		gittest.Run(t, task.WorkspaceRoot, "commit", "--author=Worker Author <worker@example.com>", "-m", "worker linked worktree commit")
	} else {
		testutil.FailErr(t, "worker edit app.txt", os.WriteFile(filepath.Join(task.WorkspaceRoot, "app.txt"), []byte("converged-edit\n"), 0o644))
		gittest.Run(t, task.WorkspaceRoot, "add", "app.txt")
		gittest.Run(t, task.WorkspaceRoot, "commit", "--author=Worker Author <worker@example.com>", "-m", "worker converged commit")
	}

	if won, err := queue.Complete(ctx, task, api.WorkerResult{Status: "complete"}); err != nil || !won {
		t.Fatalf("Complete: won=%v err=%v", won, err)
	}

	service := &MergeService{
		Queue:    queue,
		Branches: queue.Branches,
		Store:    queueServices.Promotions,
		DataDir:  dataDir,
	}
	roots := service.taskRootRefs(ctx, task)
	return linkedWorktreePromoteFixture{
		primaryDir:  primaryDir,
		worktreeDir: worktreeDir,
		adminDir:    adminDir,
		service:     service,
		task:        task,
		jobID:       jobID,
		roots:       roots,
	}
}

func TestPromoteOverlayOnRegisteredLinkedWorktree(t *testing.T) {
	ctx := t.Context()
	f := setupLinkedWorktreePromoteFixture(t, true)

	// Workspace write authority must remain strictly the registered linked worktree root.
	promote := PromoteRootsForTask(f.task, f.roots)
	if len(promote.Roots) != 1 || filepath.Clean(promote.Roots[0].Path) != filepath.Clean(f.worktreeDir) {
		t.Fatalf("PromoteRoots expanded beyond registered worktree: %+v", promote.Roots)
	}

	result, err := f.service.PromoteOverlay(ctx, "parent", f.jobID, api.PromoteOverlayInput{})
	testutil.FailErr(t, "PromoteOverlay", err)
	if result.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("PromoteOverlay status = %q, want %q (result=%+v)", result.Status, api.WorkerMergeStatusMerged, result)
	}

	// Linked worktree checkout receives the worker's bytes.
	gotApp, err := os.ReadFile(filepath.Join(f.worktreeDir, "app.txt"))
	testutil.FailErr(t, "read worktree app.txt", err)
	if string(gotApp) != "worker-edit\n" {
		t.Fatalf("worktree app.txt = %q, want %q", string(gotApp), "worker-edit\n")
	}
	gotAdded, err := os.ReadFile(filepath.Join(f.worktreeDir, "worker_added.txt"))
	testutil.FailErr(t, "read worktree worker_added.txt", err)
	if string(gotAdded) != "added-by-worker\n" {
		t.Fatalf("worktree worker_added.txt = %q, want %q", string(gotAdded), "added-by-worker\n")
	}

	// Primary repo checkout tree must remain untouched.
	primaryApp, err := os.ReadFile(filepath.Join(f.primaryDir, "app.txt"))
	testutil.FailErr(t, "read primary app.txt", err)
	if string(primaryApp) != "primary-base\n" {
		t.Fatalf("primary app.txt modified = %q, want %q", string(primaryApp), "primary-base\n")
	}
	if _, err := os.Stat(filepath.Join(f.primaryDir, "worker_added.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("primary worker_added.txt unexpectedly exists: err=%v", err)
	}

	// Imported worker checkpoint Git refs must preserve worker authorship in the shared Git repo.
	refsOut := strings.TrimSpace(gittest.Run(t, f.worktreeDir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/lycaon/workers/"))
	if refsOut == "" {
		t.Fatalf("expected imported refs under refs/lycaon/workers/, got none")
	}
	var wtBranchOID string
	for _, line := range strings.Split(refsOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.HasSuffix(fields[0], "/heads/wt-branch") {
			wtBranchOID = fields[1]
			break
		}
	}
	if wtBranchOID == "" {
		t.Fatalf("expected imported wt-branch ref under refs/lycaon/workers/, got:\n%s", refsOut)
	}
	authorOut := strings.TrimSpace(gittest.Run(t, f.worktreeDir, "log", "-1", "--format=%an <%ae> %s", wtBranchOID))
	if authorOut != "Worker Author <worker@example.com> worker linked worktree commit" {
		t.Fatalf("imported commit authorship = %q, want worker commit authorship", authorOut)
	}
}

func TestPromoteOverlayEmptyOnRegisteredLinkedWorktree(t *testing.T) {
	ctx := t.Context()
	f := setupLinkedWorktreePromoteFixture(t, false)
	// Converge the linked worktree to the exact worker bytes so PromoteOverlay takes the
	// finalizeEmptyPromote path while still importing checkpoint Git refs.
	testutil.FailErr(t, "converge worktree app.txt", os.WriteFile(filepath.Join(f.worktreeDir, "app.txt"), []byte("converged-edit\n"), 0o644))

	result, err := f.service.PromoteOverlay(ctx, "parent", f.jobID, api.PromoteOverlayInput{})
	testutil.FailErr(t, "PromoteOverlay empty", err)
	if result.Status != api.WorkerMergeStatusMerged {
		t.Fatalf("empty PromoteOverlay status = %q, want %q (result=%+v)", result.Status, api.WorkerMergeStatusMerged, result)
	}

	primaryApp, err := os.ReadFile(filepath.Join(f.primaryDir, "app.txt"))
	testutil.FailErr(t, "read primary app.txt", err)
	if string(primaryApp) != "primary-base\n" {
		t.Fatalf("primary app.txt = %q, want %q", string(primaryApp), "primary-base\n")
	}
}

func TestPromoteOverlayRefusesInvalidOrTamperedGitMetadata(t *testing.T) {
	cases := []struct {
		name   string
		tamper func(t *testing.T, f linkedWorktreePromoteFixture)
	}{
		{
			name: "unknown external gitdir",
			tamper: func(t *testing.T, f linkedWorktreePromoteFixture) {
				external := filepath.Join(t.TempDir(), "external-repo")
				testutil.FailErr(t, "mkdir external", os.MkdirAll(external, 0o755))
				gittest.Init(t, external)
				testutil.FailErr(t, "rewrite .git to external", os.WriteFile(
					filepath.Join(f.worktreeDir, ".git"),
					[]byte("gitdir: "+filepath.Join(external, ".git")+"\n"),
					0o644,
				))
			},
		},
		{
			name: "symlinked .git",
			tamper: func(t *testing.T, f linkedWorktreePromoteFixture) {
				dotGit := filepath.Join(f.worktreeDir, ".git")
				testutil.FailErr(t, "remove .git", os.Remove(dotGit))
				testutil.FailErr(t, "symlink .git", os.Symlink(f.adminDir, dotGit))
			},
		},
		{
			name: "changed back-reference in worktree gitdir",
			tamper: func(t *testing.T, f linkedWorktreePromoteFixture) {
				otherDotGit := filepath.Join(t.TempDir(), "other-wt", ".git")
				testutil.FailErr(t, "tamper gitdir back-ref", os.WriteFile(
					filepath.Join(f.adminDir, "gitdir"),
					[]byte(otherDotGit+"\n"),
					0o644,
				))
			},
		},
		{
			name: "detached commondir removed",
			tamper: func(t *testing.T, f linkedWorktreePromoteFixture) {
				testutil.FailErr(t, "remove commondir", os.Remove(filepath.Join(f.adminDir, "commondir")))
			},
		},
		{
			name: "tampered foreign commondir",
			tamper: func(t *testing.T, f linkedWorktreePromoteFixture) {
				foreign := filepath.Join(t.TempDir(), "foreign-repo")
				testutil.FailErr(t, "mkdir foreign", os.MkdirAll(foreign, 0o755))
				gittest.Init(t, foreign)
				testutil.FailErr(t, "tamper commondir", os.WriteFile(
					filepath.Join(f.adminDir, "commondir"),
					[]byte(filepath.Join(foreign, ".git")+"\n"),
					0o644,
				))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			f := setupLinkedWorktreePromoteFixture(t, true)
			tc.tamper(t, f)

			if got := authorizedPromoteGitDirs(f.task, f.roots); len(got) != 0 {
				t.Fatalf("authorizedPromoteGitDirs = %v, want empty for tampered metadata", got)
			}

			_, err := f.service.PromoteOverlay(ctx, "parent", f.jobID, api.PromoteOverlayInput{})
			if !errors.Is(err, checkpoint.ErrUnauthorizedGitDir) {
				t.Fatalf("PromoteOverlay err = %v, want ErrUnauthorizedGitDir", err)
			}

			// Failed promotion must roll back cleanly: neither linked worktree nor primary tree changed.
			gotWT, rErr := os.ReadFile(filepath.Join(f.worktreeDir, "app.txt"))
			testutil.FailErr(t, "read worktree app.txt", rErr)
			if string(gotWT) != "primary-base\n" {
				t.Fatalf("worktree app.txt after refusal = %q, want rollback to %q", string(gotWT), "primary-base\n")
			}
			gotPrimary, rErr := os.ReadFile(filepath.Join(f.primaryDir, "app.txt"))
			testutil.FailErr(t, "read primary app.txt", rErr)
			if string(gotPrimary) != "primary-base\n" {
				t.Fatalf("primary app.txt after refusal = %q, want %q", string(gotPrimary), "primary-base\n")
			}
		})
	}
}

func TestRecoverPromoteTransactionOnRegisteredLinkedWorktree(t *testing.T) {
	ctx := t.Context()
	f := setupLinkedWorktreePromoteFixture(t, true)

	// Simulate an interrupted promotion using the actual journal path: apply checkpoint
	// (files + Git refs) and leave the transaction uncommitted on disk.
	promote := PromoteRootsForTask(f.task, f.roots)
	dataDir := f.service.hostDataDirFor(f.task)
	tx, err := beginPromoteTransactionWith(dataDir, f.jobID, nil, checkpoint.TransactionConfig{
		AuthorizedRootGitDirs: authorizedPromoteGitDirs(f.task, f.roots),
	})
	testutil.FailErr(t, "beginPromoteTransactionWith", err)
	testutil.FailErr(t, "applyTaskCheckpoint", applyTaskCheckpoint(ctx, f.service, tx, f.task, "", nil))

	// Confirm the uncommitted apply landed files and imported refs before recovery.
	gotApp, err := os.ReadFile(filepath.Join(f.worktreeDir, "app.txt"))
	testutil.FailErr(t, "read applied app.txt", err)
	if string(gotApp) != "worker-edit\n" {
		t.Fatalf("applied app.txt = %q, want %q", string(gotApp), "worker-edit\n")
	}
	if refs := strings.TrimSpace(gittest.Run(t, f.worktreeDir, "for-each-ref", "refs/lycaon/workers/")); refs == "" {
		t.Fatalf("expected imported refs before recovery")
	}

	// Recover via the actual journal path with the same registered root identity.
	found, err := recoverPromoteTransaction(ctx, dataDir, f.jobID, promote, f.task)
	testutil.FailErr(t, "recoverPromoteTransaction", err)
	if !found {
		t.Fatalf("recoverPromoteTransaction found = false, want true")
	}

	// Linked worktree files and imported Git refs must be rolled back; primary repo stays untouched.
	restoredApp, err := os.ReadFile(filepath.Join(f.worktreeDir, "app.txt"))
	testutil.FailErr(t, "read restored app.txt", err)
	if string(restoredApp) != "primary-base\n" {
		t.Fatalf("restored app.txt = %q, want %q", string(restoredApp), "primary-base\n")
	}
	if _, err := os.Stat(filepath.Join(f.worktreeDir, "worker_added.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worker_added.txt still exists after recovery: err=%v", err)
	}
	if refs := strings.TrimSpace(gittest.Run(t, f.worktreeDir, "for-each-ref", "refs/lycaon/")); refs != "" {
		t.Fatalf("expected refs/lycaon/ rolled back after recovery, got:\n%s", refs)
	}
	primaryApp, err := os.ReadFile(filepath.Join(f.primaryDir, "app.txt"))
	testutil.FailErr(t, "read primary app.txt", err)
	if string(primaryApp) != "primary-base\n" {
		t.Fatalf("primary app.txt = %q, want %q", string(primaryApp), "primary-base\n")
	}
}

func TestRecoverPromoteTransactionRefusesChangedGitMetadataAcrossRestart(t *testing.T) {
	ctx := t.Context()
	f := setupLinkedWorktreePromoteFixture(t, true)

	// Apply checkpoint into the actual journal and leave it uncommitted (simulating crash before commit).
	promote := PromoteRootsForTask(f.task, f.roots)
	dataDir := f.service.hostDataDirFor(f.task)
	tx, err := beginPromoteTransactionWith(dataDir, f.jobID, nil, checkpoint.TransactionConfig{
		AuthorizedRootGitDirs: authorizedPromoteGitDirs(f.task, f.roots),
	})
	testutil.FailErr(t, "beginPromoteTransactionWith", err)
	testutil.FailErr(t, "applyTaskCheckpoint", applyTaskCheckpoint(ctx, f.service, tx, f.task, "", nil))

	// Before restart/recovery, swap worktreeDir/.git to a DIFFERENT structurally valid
	// linked worktree admin dir pointing back to worktreeDir/.git.
	otherBase := t.TempDir()
	otherRepo := filepath.Join(otherBase, "other-repo")
	testutil.FailErr(t, "mkdir other-repo", os.MkdirAll(otherRepo, 0o755))
	testutil.FailErr(t, "seed other-repo", os.WriteFile(filepath.Join(otherRepo, "other.txt"), []byte("other\n"), 0o644))
	gittest.InitCommit(t, otherRepo, "other init")
	otherAdmin := filepath.Join(otherRepo, ".git", "worktrees", "wt-branch")
	testutil.FailErr(t, "mkdir other admin", os.MkdirAll(otherAdmin, 0o755))
	dotGit := filepath.Join(f.worktreeDir, ".git")
	testutil.FailErr(t, "write other back-ref", os.WriteFile(filepath.Join(otherAdmin, "gitdir"), []byte(dotGit+"\n"), 0o644))
	testutil.FailErr(t, "write other commondir", os.WriteFile(filepath.Join(otherAdmin, "commondir"), []byte("../..\n"), 0o644))
	testutil.FailErr(t, "swap worktree .git to otherRepo", os.WriteFile(dotGit, []byte("gitdir: "+otherAdmin+"\n"), 0o644))

	// Recovery must refuse with typed ErrUnauthorizedGitDir because the durable journal
	// recorded the original gitdir/commondir identity, not otherRepo.
	found, err := recoverPromoteTransaction(ctx, dataDir, f.jobID, promote, f.task)
	if !found {
		t.Fatalf("recoverPromoteTransaction found = false, want true")
	}
	if !errors.Is(err, checkpoint.ErrUnauthorizedGitDir) {
		t.Fatalf("recoverPromoteTransaction err = %v, want ErrUnauthorizedGitDir", err)
	}
	if refs := strings.TrimSpace(gittest.Run(t, otherRepo, "for-each-ref", "refs/lycaon/")); refs != "" {
		t.Fatalf("foreign repo unexpectedly mutated during refused recovery: %s", refs)
	}
}

func TestAuthorizedPromoteGitDirsIsRootScoped(t *testing.T) {
	base := t.TempDir()
	primaryDir := filepath.Join(base, "primary-repo")
	wtA := filepath.Join(base, "wt-a")
	rootB := filepath.Join(base, "root-b")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primaryDir, 0o755))
	testutil.FailErr(t, "seed primary", os.WriteFile(filepath.Join(primaryDir, "a.txt"), []byte("a\n"), 0o644))
	gittest.InitCommit(t, primaryDir, "init")
	gittest.Run(t, primaryDir, "worktree", "add", "-b", "branch-a", wtA)
	adminA := filepath.Join(primaryDir, ".git", "worktrees", "wt-a")

	// Root B borrows Root A's administrative directory (cross-root impostor).
	testutil.FailErr(t, "mkdir rootB", os.MkdirAll(rootB, 0o755))
	testutil.FailErr(t, "write impostor .git", os.WriteFile(filepath.Join(rootB, ".git"), []byte("gitdir: "+adminA+"\n"), 0o644))

	roots := []projectroot.RootRef{
		{ID: "root-a", Path: wtA, IsPrimary: true},
		{ID: "root-b", Path: rootB},
	}
	auth := authorizedPromoteGitDirs(nil, roots)
	if _, ok := auth["root-a"]; !ok {
		t.Fatalf("expected root-a to be authorized, got %+v", auth)
	}
	if _, ok := auth["root-b"]; ok {
		t.Fatalf("expected cross-root impostor root-b to be excluded, got %+v", auth)
	}
}
