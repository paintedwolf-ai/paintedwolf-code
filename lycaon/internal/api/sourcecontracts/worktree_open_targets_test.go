package sourcecontracts

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/projectroot"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func readComparisonSideText(t *testing.T, srv *hostapi.Server, projectID string, selector wire.SourceComparisonSelector, mode string) (wire.SourceComparisonView, string) {
	t.Helper()
	view := contractfixture.ComparisonViewForTest(t, srv, projectID, selector, mode)
	if view.State != "ready" {
		t.Fatalf("comparison view state = %q, failure = %+v", view.State, view.Failure)
	}
	var text strings.Builder
	for offset := 0; int64(offset) < view.Extent.Rows; {
		frame := contractfixture.ComparisonRowsForTest(t, srv, projectID, view, offset)
		for _, row := range frame.Rows {
			text.WriteString(row.Text)
		}
		if frame.Span.End <= int64(offset) {
			t.Fatal("comparison frame did not advance")
		}
		offset = int(frame.Span.End)
	}
	return view, text.String()
}

func TestWorktreeOpenTargetsHistoricalDiffsRenamesAndSiblingProvenance(t *testing.T) {
	baseDir := gitrepo.CanonicalDir(t.TempDir())
	primaryDir := filepath.Join(baseDir, "primary")
	workerDir := filepath.Join(baseDir, "worker")
	movedWorkerDir := filepath.Join(baseDir, "worker-moved")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(filepath.Join(primaryDir, "pkg"), 0o755))

	runGitCmd(t, primaryDir, "init", "-b", "main")
	testutil.FailErr(t, "seed staged.go", os.WriteFile(filepath.Join(primaryDir, "pkg", "staged.go"), []byte("package pkg\nconst staged = \"v1\"\n"), 0o644))
	testutil.FailErr(t, "seed unstaged.go", os.WriteFile(filepath.Join(primaryDir, "pkg", "unstaged.go"), []byte("package pkg\nconst unstaged = \"v1\"\n"), 0o644))
	testutil.FailErr(t, "seed old_name.go", os.WriteFile(filepath.Join(primaryDir, "pkg", "old_name.go"), []byte("package pkg\n\n// Shared header line 1.\n// Shared header line 2.\n// Shared header line 3.\nconst renamed = \"v1\"\n"), 0o644))
	runGitCmd(t, primaryDir, "add", ".")
	runGitCmd(t, primaryDir, "commit", "-m", "initial")

	runGitCmd(t, primaryDir, "worktree", "add", "-b", "feat/worker", workerDir, "HEAD")

	// In worker checkout:
	// 1. Modify staged.go and stage it.
	// 2. Rename old_name.go -> new_name.go, edit it, and stage the rename.
	// 3. Modify unstaged.go in the working tree without staging.
	testutil.FailErr(t, "edit staged.go", os.WriteFile(filepath.Join(workerDir, "pkg", "staged.go"), []byte("package pkg\nconst staged = \"worker-staged-v2\"\n"), 0o644))
	runGitCmd(t, workerDir, "add", "pkg/staged.go")

	runGitCmd(t, workerDir, "mv", "pkg/old_name.go", "pkg/new_name.go")
	testutil.FailErr(t, "edit new_name.go", os.WriteFile(filepath.Join(workerDir, "pkg", "new_name.go"), []byte("package pkg\n\n// Shared header line 1.\n// Shared header line 2.\n// Shared header line 3.\nconst renamed = \"worker-renamed-v2\"\n"), 0o644))
	runGitCmd(t, workerDir, "add", "pkg/new_name.go")

	testutil.FailErr(t, "edit unstaged.go", os.WriteFile(filepath.Join(workerDir, "pkg", "unstaged.go"), []byte("package pkg\nconst unstaged = \"worker-unstaged-v2\"\n"), 0o644))

	ledger, database, withLedger := contractfixture.TestSourceLedger(t)
	docStore := editordoc.NewStore(database)
	var docs *editordoc.Service
	srv := contractfixture.NewTestServer(t, withLedger, contractfixture.WithSessionStore(sessionstore.NewSQL(database)), func(d *hostapi.Dependencies) {
		docs = editordoc.New(docStore, ledger, ledger.History, d.Core.Projects)
		d.Source.EditorDocuments = docs
	})
	t.Cleanup(func() { _ = docs.Close(context.Background()) })

	proj, err := srv.Sources.Workspace.ProjectRegistry.Create(t.Context(), project.CreateParams{
		Roots: []project.AttachRootParams{{Path: primaryDir, Label: "repo"}},
	})
	testutil.FailErr(t, "create project", err)
	rootID := proj.Roots[0].ID

	reposView := callJSON[wire.GitReposView](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/git/repos", nil, http.StatusOK)
	repoID := reposView.Repos[0].RepoID
	wtView := callJSON[wire.GitWorktreesView](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/git/repos/"+repoID+"/worktrees", nil, http.StatusOK)
	var workerWT wire.GitWorktreeEntry
	for _, entry := range wtView.Worktrees {
		if !entry.IsProjectCheckout {
			workerWT = entry
		}
	}
	if workerWT.WorktreeID == "" {
		t.Fatalf("missing worker worktree in %+v", wtView.Worktrees)
	}

	// Verify sibling activity path recording carries verified repo_id, worktree_id, and mapped root_id + path.
	inv := worktrees.New(database, worktrees.Options{})
	recorder := worktrees.NewToolUsageRecorder(inv)
	out := &tools.ToolInvocationOut{}
	tc := tools.ToolContext{
		Identity: tools.InvocationIdentity{ProjectID: proj.ID, SessionID: "sess-1", RootSessionID: "sess-1"},
		Source: tools.InvocationSource{
			Roots:         []projectroot.RootRef{{ID: rootID, Label: "repo", Path: primaryDir}},
			CheckoutUsage: recorder,
		},
		Effects: tools.InvocationEffects{Out: out},
	}
	tc.RecordSourcePath(filepath.Join(workerDir, "pkg", "unstaged.go"), wire.NavigationEntryKindFile)
	if out.SourceContext == nil || len(out.SourceContext.Locations) != 1 {
		t.Fatalf("recorded locations = %+v, want 1", out.SourceContext)
	}
	recordedLoc := out.SourceContext.Locations[0]
	if recordedLoc.RepoID != repoID || recordedLoc.WorktreeID != workerWT.WorktreeID || recordedLoc.RootID != rootID || recordedLoc.Path != "pkg/unstaged.go" {
		t.Fatalf("sibling recorded location = %+v, want repo=%s worktree=%s root=%s path=pkg/unstaged.go", recordedLoc, repoID, workerWT.WorktreeID, rootID)
	}

	// Capture staged and unstaged diffs from the worker checkout.
	stagedDiff := callJSON[wire.GitDiffView](t, srv, http.MethodGet,
		"/v1/projects/"+proj.ID+"/git/repos/"+repoID+"/diff?mode=staged&worktree_id="+url.QueryEscape(workerWT.WorktreeID), nil, http.StatusOK)
	unstagedDiff := callJSON[wire.GitDiffView](t, srv, http.MethodGet,
		"/v1/projects/"+proj.ID+"/git/repos/"+repoID+"/diff?mode=unstaged&worktree_id="+url.QueryEscape(workerWT.WorktreeID), nil, http.StatusOK)

	stagedByPath := make(map[string]wire.GitDiffStatItem, len(stagedDiff.Files))
	for _, item := range stagedDiff.Files {
		stagedByPath[item.Path] = item
	}
	stagedItem, ok := stagedByPath["pkg/staged.go"]
	if !ok || stagedItem.BeforeBlobOID == "" || stagedItem.AfterBlobOID == "" {
		t.Fatalf("staged diff item missing blob OIDs: %+v", stagedByPath)
	}
	renamedItem, ok := stagedByPath["pkg/new_name.go"]
	if !ok || renamedItem.OldPath != "pkg/old_name.go" || renamedItem.OldRootID != rootID || renamedItem.OldRootRelativePath != "pkg/old_name.go" || renamedItem.BeforeBlobOID == "" || renamedItem.AfterBlobOID == "" {
		t.Fatalf("renamed staged diff item = %+v", renamedItem)
	}

	if len(unstagedDiff.Files) != 1 {
		t.Fatalf("unstaged diff files = %+v, want 1", unstagedDiff.Files)
	}
	unstagedItem := unstagedDiff.Files[0]
	if unstagedItem.Path != "pkg/unstaged.go" || unstagedItem.BeforeBlobOID == "" || unstagedItem.AfterSHA256 == nil || *unstagedItem.AfterSHA256 == "" {
		t.Fatalf("unstaged diff item missing before_blob_oid or after_sha256: %+v", unstagedItem)
	}

	// Verify staged rename comparison sides via CommitComparisonSource and BlobComparisonSource.
	stagedRenameCommitSel := wire.SourceComparisonSelector{
		Commit: &wire.CommitComparisonSource{
			Kind:       "commit",
			RootID:     rootID,
			WorktreeID: workerWT.WorktreeID,
			Stage:      "staged",
			Path:       "pkg/new_name.go",
			BeforePath: "pkg/old_name.go",
		},
	}
	_, renameBeforeText := readComparisonSideText(t, srv, proj.ID, stagedRenameCommitSel, "before")
	_, renameAfterText := readComparisonSideText(t, srv, proj.ID, stagedRenameCommitSel, "after")
	if !strings.Contains(renameBeforeText, `renamed = "v1"`) || !strings.Contains(renameAfterText, `renamed = "worker-renamed-v2"`) {
		t.Fatalf("staged rename sides mismatch: before=%q after=%q", renameBeforeText, renameAfterText)
	}

	// Verify unstaged comparison before mutations returns exact index and working-tree bytes.
	unstagedHistoricalSel := wire.SourceComparisonSelector{
		Commit: &wire.CommitComparisonSource{
			Kind:                "commit",
			RootID:              rootID,
			WorktreeID:          workerWT.WorktreeID,
			Stage:               "unstaged",
			Path:                "pkg/unstaged.go",
			BeforeBlobOID:       unstagedItem.BeforeBlobOID,
			ExpectedHead:        &unstagedDiff.HeadSHA,
			ExpectedAfterSHA256: unstagedItem.AfterSHA256,
		},
	}
	_, unstagedBefore0 := readComparisonSideText(t, srv, proj.ID, unstagedHistoricalSel, "before")
	_, unstagedAfter0 := readComparisonSideText(t, srv, proj.ID, unstagedHistoricalSel, "after")
	if !strings.Contains(unstagedBefore0, `unstaged = "v1"`) || !strings.Contains(unstagedAfter0, `unstaged = "worker-unstaged-v2"`) {
		t.Fatalf("initial unstaged sides mismatch: before=%q after=%q", unstagedBefore0, unstagedAfter0)
	}

	stagedBlobSel := wire.SourceComparisonSelector{
		Blob: &wire.BlobComparisonSource{
			Kind:          "blob",
			RootID:        rootID,
			BeforeBlobOid: stagedItem.BeforeBlobOID,
			BlobOid:       stagedItem.AfterBlobOID,
			DisplayPath:   "pkg/staged.go",
		},
	}

	verifyHistoricalDiffStabilityAcrossMutations(t, srv, proj.ID, primaryDir, workerDir, movedWorkerDir, stagedBlobSel, unstagedHistoricalSel)
}

func verifyHistoricalDiffStabilityAcrossMutations(
	t *testing.T,
	srv *hostapi.Server,
	projectID, primaryDir, workerDir, movedWorkerDir string,
	stagedBlobSel, unstagedHistoricalSel wire.SourceComparisonSelector,
) {
	t.Helper()

	// Mutation 1: Branch switch and pathname reuse with different content in worker checkout.
	runGitCmd(t, workerDir, "checkout", "-f", "-b", "feat/other-branch", "HEAD")
	testutil.FailErr(t, "reuse staged.go", os.WriteFile(filepath.Join(workerDir, "pkg", "staged.go"), []byte("package pkg\nconst staged = \"TODAY-UNRELATED\"\n"), 0o644))
	testutil.FailErr(t, "reuse unstaged.go", os.WriteFile(filepath.Join(workerDir, "pkg", "unstaged.go"), []byte("package pkg\nconst unstaged = \"TODAY-UNRELATED\"\n"), 0o644))

	_, stagedBeforeAfterSwitch := readComparisonSideText(t, srv, projectID, stagedBlobSel, "before")
	_, stagedAfterAfterSwitch := readComparisonSideText(t, srv, projectID, stagedBlobSel, "after")
	if !strings.Contains(stagedBeforeAfterSwitch, `staged = "v1"`) || !strings.Contains(stagedAfterAfterSwitch, `staged = "worker-staged-v2"`) {
		t.Fatalf("staged blob diff corrupted after branch switch and reuse: before=%q after=%q", stagedBeforeAfterSwitch, stagedAfterAfterSwitch)
	}

	reusedView := contractfixture.ComparisonViewForTest(t, srv, projectID, unstagedHistoricalSel, "changes")
	if reusedView.Comparison == nil || reusedView.Comparison.After == nil {
		t.Fatalf("missing comparison details after reuse: %+v", reusedView)
	}
	if reusedView.Comparison.After.Availability != "unavailable" {
		t.Fatalf("after pathname reuse, expected after availability=unavailable, got %+v", reusedView.Comparison.After)
	}
	if reusedView.Comparison.Before == nil || reusedView.Comparison.Before.Availability != "available" {
		t.Fatalf("expected before blob to remain available from Git object store, got %+v", reusedView.Comparison.Before)
	}

	// Mutation 2: Delete pkg/unstaged.go on disk and verify after stays unavailable.
	testutil.FailErr(t, "delete unstaged.go", os.Remove(filepath.Join(workerDir, "pkg", "unstaged.go")))
	deletedView := contractfixture.ComparisonViewForTest(t, srv, projectID, unstagedHistoricalSel, "changes")
	if deletedView.Comparison == nil || deletedView.Comparison.After == nil || deletedView.Comparison.After.Availability != "unavailable" {
		t.Fatalf("after deletion, expected after availability=unavailable, got %+v", deletedView.Comparison)
	}

	// Mutation 3: Move the checkout directory, then remove the checkout entirely.
	runGitCmd(t, primaryDir, "worktree", "move", workerDir, movedWorkerDir)
	movedView := contractfixture.ComparisonViewForTest(t, srv, projectID, unstagedHistoricalSel, "changes")
	if movedView.Comparison == nil || movedView.Comparison.After == nil || movedView.Comparison.After.Availability != "unavailable" {
		t.Fatalf("after checkout move, expected after availability=unavailable, got %+v", movedView.Comparison)
	}
	runGitCmd(t, primaryDir, "worktree", "remove", "--force", movedWorkerDir)

	_, stagedAfterRemoval := readComparisonSideText(t, srv, projectID, stagedBlobSel, "after")
	if !strings.Contains(stagedAfterRemoval, `staged = "worker-staged-v2"`) {
		t.Fatalf("staged blob diff after checkout removal = %q", stagedAfterRemoval)
	}
	removedView := contractfixture.ComparisonViewForTest(t, srv, projectID, unstagedHistoricalSel, "changes")
	if removedView.Comparison == nil || removedView.Comparison.After == nil || removedView.Comparison.After.Availability != "unavailable" {
		t.Fatalf("after checkout removal, expected after availability=unavailable, got %+v", removedView.Comparison)
	}
}
