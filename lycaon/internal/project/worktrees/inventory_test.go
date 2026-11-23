package worktrees_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
)

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "-C", dir}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, string(out))
	}
}

func initGitFixture(t *testing.T) (base, primary string) {
	t.Helper()
	base = gitrepo.CanonicalDir(t.TempDir())
	primary = filepath.Join(base, "primary")
	testutil.FailErr(t, "mkdir primary/packages/a", os.MkdirAll(filepath.Join(primary, "packages", "a"), 0o755))
	runGitCmd(t, primary, "init", "-q", "-b", "main")
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(primary, "packages", "a", "app.go"), []byte("package a\n"), 0o644))
	runGitCmd(t, primary, "add", ".")
	runGitCmd(t, primary, "commit", "-qm", "initial")
	return base, primary
}

func openInventoryStore(t *testing.T, rootDir string) *db.DB {
	t.Helper()
	storeDir := t.TempDir()
	sqlDB := testdbfixture.OpenPath(t, filepath.Join(storeDir, "store.db"))
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, rootDir)
	return sqlDB
}

func findCheckoutByPath(items []worktrees.Checkout, path string) (worktrees.Checkout, bool) {
	want := gitrepo.CanonicalDir(path)
	if want == "" {
		want = filepath.Clean(path)
	}
	for _, item := range items {
		if item.WorktreePath == want {
			return item, true
		}
	}
	return worktrees.Checkout{}, false
}

func TestInventoryAcrossMovesPathReuseBranchReuseAndRestart(t *testing.T) {
	t.Parallel()
	base, primary := initGitFixture(t)
	sqlDB := openInventoryStore(t, filepath.Join(primary, "packages", "a"))
	repoID := git.DeriveRepoID(primary)
	spec := worktrees.RepoSpec{
		ProjectID: testdbseed.DefaultProjectID,
		RepoID:    repoID,
		Toplevel:  primary,
	}

	spaced := filepath.Join(base, "sibling with space")
	runGitCmd(t, primary, "worktree", "add", "-q", "-b", "feat/work", spaced)
	detached := filepath.Join(base, "detached-wt")
	runGitCmd(t, primary, "worktree", "add", "-q", "--detach", detached)
	relative := filepath.Join(base, "relative-wt")
	runGitCmd(t, primary, "worktree", "add", "-q", "--relative-paths", "-b", "feat/rel", relative)

	inv1 := worktrees.New(sqlDB, worktrees.Options{})
	items, err := inv1.Read(t.Context(), spec)
	testutil.FailErr(t, "initial inventory read", err)
	if len(items) != 4 {
		t.Fatalf("got %d checkouts, want 4: %+v", len(items), items)
	}

	primRow, ok := findCheckoutByPath(items, primary)
	if !ok || !primRow.IsProjectCheckout() || primRow.SourceBranch() != sourcebranch.Trunk || primRow.Kind != gitrepo.CheckoutPrimary {
		t.Fatalf("unexpected primary checkout: %+v (ok=%v)", primRow, ok)
	}
	spacedRow, ok := findCheckoutByPath(items, spaced)
	if !ok || spacedRow.IsProjectCheckout() || spacedRow.Branch != "feat/work" || spacedRow.Kind != gitrepo.CheckoutLinked {
		t.Fatalf("unexpected spaced checkout: %+v (ok=%v)", spacedRow, ok)
	}
	detachedRow, ok := findCheckoutByPath(items, detached)
	if !ok || !detachedRow.Detached || detachedRow.Branch != "" {
		t.Fatalf("unexpected detached checkout: %+v (ok=%v)", detachedRow, ok)
	}

	// Move `spaced` to `moved`, then simulate a full host restart before reconciling.
	moved := filepath.Join(base, "moved-sibling")
	runGitCmd(t, primary, "worktree", "move", spaced, moved)

	inv2 := worktrees.New(sqlDB, worktrees.Options{})
	afterMove, err := inv2.Read(t.Context(), spec)
	testutil.FailErr(t, "inventory after move and restart", err)
	movedRow, ok := findCheckoutByPath(afterMove, moved)
	if !ok {
		t.Fatalf("moved checkout missing from %+v", afterMove)
	}
	if movedRow.ID != spacedRow.ID {
		t.Fatalf("proven move changed checkout ID across restart: %s -> %s", spacedRow.ID, movedRow.ID)
	}

	// Record an editor draft on movedRow's branch, then remove moved and recreate at the original path AND on the same branch.
	rootID := seedDraftForCheckout(t, sqlDB, filepath.Join(primary, "packages", "a"), movedRow.ID, "app.go", "draft from first incarnation\n")

	runGitCmd(t, primary, "worktree", "remove", moved)
	// Reuse the exact same path `spaced` and the exact same branch `feat/work`.
	runGitCmd(t, primary, "worktree", "add", "-q", spaced, "feat/work")

	// Before inventory refreshes, ResolveBranch on the old ID must already reject the replaced incarnation.
	proj := &project.Project{
		ID:    testdbseed.DefaultProjectID,
		Roots: []project.Root{{ID: rootID, Path: filepath.Join(primary, "packages", "a"), Label: "a", IsPrimary: true}},
	}
	if _, resolveErr := project.ResolveBranch(t.Context(), sqlDB, proj, sourcebranch.ForWorktree(movedRow.ID)); !errors.Is(resolveErr, worktrees.ErrCheckoutRetired) && !errors.Is(resolveErr, worktrees.ErrCheckoutUnavailable) {
		t.Fatalf("ResolveBranch accepted replaced incarnation before refresh: %v", resolveErr)
	}

	// Simulate another restart and reconcile; replacement must receive a fresh ID while keeping the old draft separately.
	inv3 := worktrees.New(sqlDB, worktrees.Options{})
	afterReuse, err := inv3.Read(t.Context(), spec)
	testutil.FailErr(t, "inventory after path/branch reuse", err)
	reusedRow, ok := findCheckoutByPath(afterReuse, spaced)
	if !ok {
		t.Fatalf("reused path missing from %+v", afterReuse)
	}
	if reusedRow.ID == movedRow.ID {
		t.Fatalf("reused path and branch inherited retired checkout ID %s", reusedRow.ID)
	}

	oldStored, err := inv3.Get(t.Context(), testdbseed.DefaultProjectID, movedRow.ID)
	testutil.FailErr(t, "get retired incarnation", err)
	if oldStored.State != "retired" || oldStored.Available {
		t.Fatalf("expected old incarnation retired, got %+v", oldStored)
	}

	// Old draft remains intact under the retired branch and never leaks onto the replacement branch.
	var draft string
	err = sqlDB.QueryRowContext(t.Context(),
		`SELECT draft FROM editor_documents WHERE project_id=? AND branch_id=? AND root_id=? AND path='app.go'`,
		testdbseed.DefaultProjectID, string(sourcebranch.ForWorktree(movedRow.ID)), rootID,
	).Scan(&draft)
	testutil.FailErr(t, "read retained draft on retired incarnation", err)
	if draft != "draft from first incarnation\n" {
		t.Fatalf("retained draft = %q", draft)
	}

	resolvedNew, err := project.ResolveBranch(t.Context(), sqlDB, proj, sourcebranch.ForWorktree(reusedRow.ID))
	testutil.FailErr(t, "resolve replacement checkout", err)
	wantProjected := filepath.Join(spaced, "packages", "a")
	if len(resolvedNew.Roots) != 1 || resolvedNew.Roots[0].Path != wantProjected {
		t.Fatalf("unexpected projected roots on replacement: %+v, want %q", resolvedNew.Roots, wantProjected)
	}
}

func TestLinkedProjectRootIncludesPrimaryAndBareExposesOnlyWorkingTrees(t *testing.T) {
	t.Parallel()
	base, primary := initGitFixture(t)
	linkedRoot := filepath.Join(base, "linked-project-root")
	runGitCmd(t, primary, "worktree", "add", "-q", "-b", "feat/linked-root", linkedRoot)
	siblingB := filepath.Join(base, "sibling-b")
	runGitCmd(t, primary, "worktree", "add", "-q", "-b", "feat/sib-b", siblingB)

	sqlDB := openInventoryStore(t, linkedRoot)
	repoID := git.DeriveRepoID(linkedRoot)
	inv := worktrees.New(sqlDB, worktrees.Options{})

	items, err := inv.Read(t.Context(), worktrees.RepoSpec{
		ProjectID: testdbseed.DefaultProjectID,
		RepoID:    repoID,
		Toplevel:  linkedRoot,
	})
	testutil.FailErr(t, "read linked-root inventory", err)
	if len(items) != 3 {
		t.Fatalf("got %d checkouts, want 3: %+v", len(items), items)
	}

	attachedRow, ok := findCheckoutByPath(items, linkedRoot)
	if !ok || !attachedRow.IsProjectCheckout() || attachedRow.SourceBranch() != sourcebranch.Trunk {
		t.Fatalf("linked project root did not map to Trunk: %+v", attachedRow)
	}
	primaryAsSibling, ok := findCheckoutByPath(items, primary)
	if !ok || primaryAsSibling.IsProjectCheckout() || primaryAsSibling.Kind != gitrepo.CheckoutPrimary || primaryAsSibling.SourceBranch() != sourcebranch.ForWorktree(primaryAsSibling.ID) {
		t.Fatalf("primary checkout not exposed as addressable target from linked root: %+v", primaryAsSibling)
	}

	// Bare common repository exposes only actual working directories.
	bareRepo := filepath.Join(base, "bare.git")
	runGitCmd(t, base, "clone", "-q", "--bare", primary, bareRepo)
	bareWT := filepath.Join(base, "bare-wt")
	runGitCmd(t, bareRepo, "worktree", "add", "-q", "-b", "feat/bare-wt", bareWT)
	bareItems, err := inv.Read(t.Context(), worktrees.RepoSpec{
		ProjectID: testdbseed.DefaultProjectID,
		RepoID:    git.DeriveRepoID(bareWT),
		Toplevel:  bareWT,
	})
	testutil.FailErr(t, "read bare-backed inventory", err)
	if len(bareItems) != 1 || bareItems[0].WorktreePath != bareWT {
		t.Fatalf("bare repository exposed non-working entry: %+v", bareItems)
	}
}

func TestAmbiguousBareInodeRestartRetiresMappingAndPreservesDraft(t *testing.T) {
	t.Parallel()
	base, primary := initGitFixture(t)
	sibling := filepath.Join(base, "sibling-bare-inode")
	runGitCmd(t, primary, "worktree", "add", "-q", "-b", "feat/inode", sibling)

	sqlDB := openInventoryStore(t, primary)
	spec := worktrees.RepoSpec{
		ProjectID: testdbseed.DefaultProjectID,
		RepoID:    git.DeriveRepoID(primary),
		Toplevel:  primary,
	}

	bareInspector := func(commonDir, listedPath string) gitrepo.RegisteredCheckout {
		reg := gitrepo.InspectListedCheckout(commonDir, listedPath)
		if reg.CommonIdentity != "" {
			reg.CommonIdentity = "1:100"
		}
		if reg.AdminIdentity != "" {
			reg.AdminIdentity = "1:200"
		}
		if reg.WorktreeIdentity != "" {
			reg.WorktreeIdentity = "1:300"
		}
		return reg
	}

	inv1 := worktrees.New(sqlDB, worktrees.Options{Inspector: bareInspector})
	first, err := inv1.Read(t.Context(), spec)
	testutil.FailErr(t, "first bare-inode read", err)
	sib1, ok := findCheckoutByPath(first, sibling)
	if !ok {
		t.Fatal("missing sibling")
	}

	// In-process continuous refresh preserves identity.
	inProc, err := inv1.Refresh(t.Context(), spec)
	testutil.FailErr(t, "in-process bare-inode refresh", err)
	sibInProc, _ := findCheckoutByPath(inProc, sibling)
	if sibInProc.ID != sib1.ID {
		t.Fatalf("continuous in-process refresh lost ID: %s -> %s", sib1.ID, sibInProc.ID)
	}

	seedDraftForCheckout(t, sqlDB, primary, sib1.ID, "packages/a/app.go", "unsaved bare-inode draft\n")

	// Restart loses in-memory continuity; bare inode numbers alone cannot establish continuity.
	invRestarted := worktrees.New(sqlDB, worktrees.Options{Inspector: bareInspector})
	afterRestart, err := invRestarted.Read(t.Context(), spec)
	testutil.FailErr(t, "bare-inode read after restart", err)
	sib2, ok := findCheckoutByPath(afterRestart, sibling)
	if !ok {
		t.Fatal("missing sibling after restart")
	}
	if sib2.ID == sib1.ID {
		t.Fatalf("bare inode established continuity across restart for %s", sib2.ID)
	}

	superseded, err := invRestarted.ListSuperseded(t.Context(), testdbseed.DefaultProjectID, spec.RepoID, sib2.ID)
	testutil.FailErr(t, "list superseded", err)
	if len(superseded) != 1 || superseded[0].ID != sib1.ID {
		t.Fatalf("expected %s linked as superseded by %s, got %+v", sib1.ID, sib2.ID, superseded)
	}
}

type blockingLister struct {
	inner   worktrees.Lister
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingLister) ListWorktrees(ctx context.Context, toplevel string) ([]git.WorktreeEntry, error) {
	n := b.calls.Add(1)
	if n == 1 && b.entered != nil {
		b.once.Do(func() { close(b.entered) })
		<-b.release
	}
	return b.inner.ListWorktrees(ctx, toplevel)
}

func TestCoalescesConcurrentConsumersAndSchedulesSingleFollowUpForUncoveredGeneration(t *testing.T) {
	t.Parallel()
	base, primary := initGitFixture(t)
	sqlDB := openInventoryStore(t, primary)
	spec := worktrees.RepoSpec{
		ProjectID: testdbseed.DefaultProjectID,
		RepoID:    git.DeriveRepoID(primary),
		Toplevel:  primary,
	}

	lister := &blockingLister{
		inner:   git.NewManager(),
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	inv := worktrees.New(sqlDB, worktrees.Options{Git: lister})

	var wg sync.WaitGroup
	results := make([][]worktrees.Checkout, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = inv.Read(t.Context(), spec)
		}(i)
	}

	<-lister.entered
	// Create a sibling and fire 15 rapid duplicate change notifications while refresh 1 is running.
	sibling := filepath.Join(base, "mid-refresh-sibling")
	runGitCmd(t, primary, "worktree", "add", "-q", "-b", "feat/mid", sibling)
	for range 15 {
		inv.NotifyChange(spec.ProjectID, spec.RepoID)
	}
	close(lister.release)
	wg.Wait()

	for i := range 2 {
		testutil.FailErr(t, "concurrent consumer read", errs[i])
	}
	if len(results[0]) != len(results[1]) || results[0][0].ID != results[1][0].ID {
		t.Fatalf("simultaneous consumers saw inconsistent inventories: %+v vs %+v", results[0], results[1])
	}

	// Read after the mid-flight notifications must already be covered by the single follow-up pass.
	latest, err := inv.Read(t.Context(), spec)
	testutil.FailErr(t, "read covered generation", err)
	if _, ok := findCheckoutByPath(latest, sibling); !ok {
		t.Fatalf("follow-up refresh missed sibling created mid-flight: %+v", latest)
	}
	if got := lister.calls.Load(); got != 2 {
		t.Fatalf("ListWorktrees call count = %d, want 2 (initial + 1 coalesced follow-up)", got)
	}
}

func seedDraftForCheckout(t *testing.T, sqlDB *db.DB, rootPath, checkoutID, relPath, draft string) string {
	t.Helper()
	var rootID string
	err := sqlDB.QueryRowContext(t.Context(),
		`SELECT id FROM project_roots WHERE project_id=? AND path=?`,
		testdbseed.DefaultProjectID, rootPath,
	).Scan(&rootID)
	testutil.FailErr(t, "lookup root_id", err)

	now := timelayout.Format(time.Now().UTC())
	fileID := uuid.NewString()
	_, err = sqlDB.ExecContext(t.Context(),
		`INSERT INTO source_files (id, project_id, entry_kind, created_ts) VALUES (?, ?, 'file', ?)`,
		fileID, testdbseed.DefaultProjectID, now,
	)
	testutil.FailErr(t, "insert source_file", err)

	docID := uuid.NewString()
	branch := string(sourcebranch.ForWorktree(checkoutID))
	_, err = sqlDB.ExecContext(t.Context(),
		`INSERT INTO editor_documents (
			branch_id, id, project_id, file_id, root_id, path, draft, base_content, base_sha256,
			encoding, size_bytes, eol, base_eol, mixed_eol, base_mixed_eol, revision, dirty, diverged,
			created_at, updated_at, opened_at, held_agent_version_id, absent
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'package a\n', '00', 'utf-8', 10, 'lf', 'lf', 0, 0, 2, 1, 0, ?, ?, ?, '', 0)`,
		branch, docID, testdbseed.DefaultProjectID, fileID, rootID, relPath, draft, now, now, now,
	)
	testutil.FailErr(t, "insert editor_document", err)
	return rootID
}
