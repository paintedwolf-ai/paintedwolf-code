package hostcontracts

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/worktrees"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/testutil/llmtest"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func getAuthedJSON[T any](t *testing.T, srv *hostapi.Server, path string, wantStatus int) T {
	t.Helper()
	req := contractfixture.NewAuthedRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != wantStatus {
		t.Fatalf("GET %s = %d want %d body=%s", path, w.Code, wantStatus, w.Body.String())
	}
	var out T
	testutil.FailErr(t, "decode "+path, json.Unmarshal(w.Body.Bytes(), &out))
	return out
}

func waitCheckoutStatus(t *testing.T, srv *hostapi.Server, projectID, repoID, worktreeID string) (wire.GitStatusSummary, wire.GitChangesPage) {
	t.Helper()
	q := ""
	if worktreeID != "" {
		q = "?worktree_id=" + worktreeID
	}
	for range 40 {
		req := contractfixture.NewAuthedRequest(http.MethodGet, fmt.Sprintf("/v1/projects/%s/git/repos/%s/status%s", projectID, repoID, q), nil)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			var summary wire.GitStatusSummary
			testutil.FailErr(t, "decode status", json.Unmarshal(w.Body.Bytes(), &summary))
			if summary.Revision != 0 {
				changesURL := fmt.Sprintf("/v1/projects/%s/git/repos/%s/changes?revision=%d", projectID, repoID, summary.Revision)
				if worktreeID != "" {
					changesURL += "&worktree_id=" + worktreeID
				}
				changes := getAuthedJSON[wire.GitChangesPage](t, srv, changesURL, http.StatusOK)
				return summary, changes
			}
		}
		srv.Admin.Git.Board.StatusCache.GetOrLoad(t.Context(), "", false)
	}
	t.Fatalf("status never converged for repo=%s worktree=%s", repoID, worktreeID)
	return wire.GitStatusSummary{}, wire.GitChangesPage{}
}

func TestGitWorktreeTargetsStatusDiffCommitDiscardCursorAndRemoval(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t, func(d *hostapi.Dependencies) {
		d.Providers.CommitDrafter = llmtest.Summarizer{
			Structured: compaction.SummaryFormatRequested,
			Text:       "feat: targeted checkout subject",
		}
	})
	p, err := srv.Sources.Workspace.ProjectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "get project", err)
	primaryRoot := gitrepo.CanonicalDir(project.PrimaryRootPath(p))
	rootID := p.Roots[0].ID
	repoID := contractfixture.WorktreeRepoID(t, srv, projectID)

	testutil.FailErr(t, "write shared.txt", os.WriteFile(filepath.Join(primaryRoot, "shared.txt"), []byte("base\n"), 0o644))
	testutil.FailErr(t, "write second.txt", os.WriteFile(filepath.Join(primaryRoot, "second.txt"), []byte("base-2\n"), 0o644))
	contractfixture.RunGitIn(t, primaryRoot, "add", "shared.txt", "second.txt")
	contractfixture.RunGitIn(t, primaryRoot, "commit", "-m", "seed shared files")
	primaryHead := strings.TrimSpace(gittest.Run(t, primaryRoot, "rev-parse", "HEAD"))

	sibParent := gitrepo.CanonicalDir(t.TempDir())
	siblingA := filepath.Join(sibParent, "wt-a")
	siblingB := filepath.Join(sibParent, "wt-b")
	contractfixture.RunGitIn(t, primaryRoot, "worktree", "add", "-b", "feat/a", siblingA)
	contractfixture.RunGitIn(t, primaryRoot, "worktree", "add", "-b", "feat/b", siblingB)

	sess := contractfixture.CreateSessionForProject(t, srv, projectID)
	_, err = srv.Admin.Git.Worktrees.RecordUsage(t.Context(), worktrees.UsageInput{
		ProjectID:      projectID,
		TargetPath:     siblingA,
		RootSessionID:  sess.ID,
		ActorSessionID: sess.ID,
		WorkerID:       "worker-a",
		ActivityID:     "act-create-a",
		Relation:       worktrees.UsageRelationCreated,
	})
	testutil.FailErr(t, "record usage A", err)

	// 1. Worktrees collection returns verified checkouts without eagerly warming StatusCache on siblings.
	wtView := getAuthedJSON[wire.GitWorktreesView](t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/worktrees?session_id=%s", projectID, repoID, sess.ID), http.StatusOK)
	if len(wtView.Worktrees) != 3 || wtView.ProjectCheckoutID == "" {
		t.Fatalf("expected 3 checkouts and project_checkout_id, got %+v", wtView)
	}
	var idPrimary, idA, idB string
	for _, entry := range wtView.Worktrees {
		switch filepath.Clean(entry.Path) {
		case filepath.Clean(primaryRoot):
			idPrimary = entry.WorktreeID
			if !entry.IsProjectCheckout || entry.Kind != "primary" {
				t.Fatalf("unexpected primary entry: %+v", entry)
			}
		case filepath.Clean(siblingA):
			idA = entry.WorktreeID
			if entry.StatusCached {
				t.Fatalf("expected uncached sibling A status before selection: %+v", entry)
			}
			if !entry.CreatedByChat || !entry.UsedByChat || len(entry.WorkerIDs) != 1 || entry.WorkerIDs[0] != "worker-a" {
				t.Fatalf("unexpected chat usage on sibling A: %+v", entry)
			}
		case filepath.Clean(siblingB):
			idB = entry.WorktreeID
			if entry.StatusCached || entry.CreatedByChat || entry.UsedByChat {
				t.Fatalf("unexpected state on external sibling B: %+v", entry)
			}
		}
	}
	if idPrimary == "" || idA == "" || idB == "" {
		t.Fatalf("missing checkout ids: primary=%q a=%q b=%q", idPrimary, idA, idB)
	}

	// 2. Stage and modify the same relative file differently in sibling A and sibling B.
	testutil.FailErr(t, "stage A", os.WriteFile(filepath.Join(siblingA, "shared.txt"), []byte("staged-A\n"), 0o644))
	contractfixture.RunGitIn(t, siblingA, "add", "shared.txt")
	testutil.FailErr(t, "unstaged A", os.WriteFile(filepath.Join(siblingA, "shared.txt"), []byte("unstaged-A\n"), 0o644))
	testutil.FailErr(t, "second A", os.WriteFile(filepath.Join(siblingA, "second.txt"), []byte("second-A\n"), 0o644))

	testutil.FailErr(t, "stage B", os.WriteFile(filepath.Join(siblingB, "shared.txt"), []byte("staged-B\n"), 0o644))
	contractfixture.RunGitIn(t, siblingB, "add", "shared.txt")
	testutil.FailErr(t, "unstaged B", os.WriteFile(filepath.Join(siblingB, "shared.txt"), []byte("unstaged-B\n"), 0o644))

	_, _ = srv.Admin.Git.Board.StatusCache.GetOrLoad(t.Context(), primaryRoot, true)
	_, _ = srv.Admin.Git.Board.StatusCache.GetOrLoad(t.Context(), siblingA, true)
	_, _ = srv.Admin.Git.Board.StatusCache.GetOrLoad(t.Context(), siblingB, true)

	sumPrimary, _ := waitCheckoutStatus(t, srv, projectID, repoID, "")
	if sumPrimary.Dirty || sumPrimary.WorktreeID != idPrimary {
		t.Fatalf("expected clean primary checkout %s, got %+v", idPrimary, sumPrimary)
	}
	sumA, changesA := waitCheckoutStatus(t, srv, projectID, repoID, idA)
	if !sumA.Dirty || sumA.WorktreeID != idA || sumA.StagedCount != 1 || sumA.UnstagedCount != 2 || len(changesA.Files) != 2 {
		t.Fatalf("unexpected sibling A status/changes: summary=%+v changes=%+v", sumA, changesA)
	}
	for _, f := range changesA.Files {
		if f.RootID != rootID || f.RootRelativePath != f.Path {
			t.Fatalf("sibling A changed file did not project onto rootID %s: %+v", rootID, f)
		}
	}
	sumB, changesB := waitCheckoutStatus(t, srv, projectID, repoID, idB)
	if !sumB.Dirty || sumB.WorktreeID != idB || sumB.StagedCount != 1 || sumB.UnstagedCount != 1 || len(changesB.Files) != 1 {
		t.Fatalf("unexpected sibling B status/changes: summary=%+v changes=%+v", sumB, changesB)
	}

	// Compare unified patches and exact before/after bytes for staged, unstaged, and commit preview.
	diffStagedA := getAuthedJSON[wire.GitDiffView](t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/diff?worktree_id=%s&mode=staged", projectID, repoID, idA), http.StatusOK)
	diffUnstagedA := getAuthedJSON[wire.GitDiffView](t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/diff?worktree_id=%s&mode=unstaged&path=shared.txt", projectID, repoID, idA), http.StatusOK)
	diffCommitB := getAuthedJSON[wire.GitDiffView](t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/diff?worktree_id=%s&mode=commit", projectID, repoID, idB), http.StatusOK)
	if !strings.Contains(diffStagedA.Patch, "+staged-A") || strings.Contains(diffStagedA.Patch, "unstaged-A") {
		t.Fatalf("unexpected staged patch A: %s", diffStagedA.Patch)
	}
	if !strings.Contains(diffUnstagedA.Patch, "-staged-A") || !strings.Contains(diffUnstagedA.Patch, "+unstaged-A") {
		t.Fatalf("unexpected unstaged patch A: %s", diffUnstagedA.Patch)
	}
	if !strings.Contains(diffCommitB.Patch, "+unstaged-B") || diffCommitB.WorktreeID != idB {
		t.Fatalf("unexpected commit preview B: %+v", diffCommitB)
	}

	cmpStagedA := getAuthedJSON[wire.SourceComparison](t, srv, fmt.Sprintf("/v1/projects/%s/source/comparison?baseline=commit&root_id=%s&path=shared.txt&worktree_id=%s&stage=staged", projectID, rootID, idA), http.StatusOK)
	cmpUnstagedA := getAuthedJSON[wire.SourceComparison](t, srv, fmt.Sprintf("/v1/projects/%s/source/comparison?baseline=commit&root_id=%s&path=shared.txt&worktree_id=%s&stage=unstaged", projectID, rootID, idA), http.StatusOK)
	cmpStagedB := getAuthedJSON[wire.SourceComparison](t, srv, fmt.Sprintf("/v1/projects/%s/source/comparison?baseline=commit&root_id=%s&path=shared.txt&worktree_id=%s&stage=staged", projectID, rootID, idB), http.StatusOK)
	if cmpStagedA.Before.Content != "base\n" || cmpStagedA.After.Content != "staged-A\n" {
		t.Fatalf("unexpected staged comparison A: before=%q after=%q", cmpStagedA.Before.Content, cmpStagedA.After.Content)
	}
	if cmpUnstagedA.Before.Content != "staged-A\n" || cmpUnstagedA.After.Content != "unstaged-A\n" {
		t.Fatalf("unexpected unstaged comparison A: before=%q after=%q", cmpUnstagedA.Before.Content, cmpUnstagedA.After.Content)
	}
	if cmpStagedB.Before.Content != "base\n" || cmpStagedB.After.Content != "staged-B\n" {
		t.Fatalf("unexpected staged comparison B: before=%q after=%q", cmpStagedB.Before.Content, cmpStagedB.After.Content)
	}

	// 3. Reusing sibling A's pagination cursor on sibling B or primary is rejected.
	page1A := getAuthedJSON[wire.GitChangesPage](t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/changes?worktree_id=%s&limit=1", projectID, repoID, idA), http.StatusOK)
	if page1A.NextCursor == "" {
		t.Fatalf("expected next_cursor for 2 changed files with limit=1: %+v", page1A)
	}
	crossB := contractfixture.NewAuthedRequest(http.MethodGet, fmt.Sprintf("/v1/projects/%s/git/repos/%s/changes?worktree_id=%s&cursor=%s", projectID, repoID, idB, page1A.NextCursor), nil)
	wCrossB := httptest.NewRecorder()
	srv.ServeHTTP(wCrossB, crossB)
	if wCrossB.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when reusing cursor across checkouts, got %d body=%s", wCrossB.Code, wCrossB.Body.String())
	}
	crossPrimary := contractfixture.NewAuthedRequest(http.MethodGet, fmt.Sprintf("/v1/projects/%s/git/repos/%s/changes?cursor=%s", projectID, repoID, page1A.NextCursor), nil)
	wCrossPrimary := httptest.NewRecorder()
	srv.ServeHTTP(wCrossPrimary, crossPrimary)
	if wCrossPrimary.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when reusing sibling cursor on omitted primary, got %d body=%s", wCrossPrimary.Code, wCrossPrimary.Body.String())
	}

	// 4. Draft commit message and commit staged_only in sibling A; discard in sibling B; primary untouched.
	draftW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/commit-message?worktree_id=%s", projectID, repoID, idA), map[string]any{})
	if draftW.Code != http.StatusOK {
		t.Fatalf("draft commit-message A = %d body=%s", draftW.Code, draftW.Body.String())
	}

	staleCommitW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/commit?worktree_id=%s", projectID, repoID, idA), map[string]any{
		"message":       "feat: stale head",
		"staged_only":   true,
		"expected_head": "0000000000000000000000000000000000000000",
	})
	if staleCommitW.Code != http.StatusConflict {
		t.Fatalf("expected 409 git_state_changed on mismatched expected_head, got %d body=%s", staleCommitW.Code, staleCommitW.Body.String())
	}

	commitAW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/commit?worktree_id=%s", projectID, repoID, idA), map[string]any{
		"message":       "feat: commit staged A",
		"staged_only":   true,
		"expected_head": diffStagedA.HeadSHA,
	})
	if commitAW.Code != http.StatusOK {
		t.Fatalf("commit staged_only A = %d body=%s", commitAW.Code, commitAW.Body.String())
	}
	headCommittedA := strings.TrimSpace(gittest.Run(t, siblingA, "show", "HEAD:shared.txt"))
	workingA, err := os.ReadFile(filepath.Join(siblingA, "shared.txt"))
	testutil.FailErr(t, "read working A", err)
	if headCommittedA != "staged-A" || string(workingA) != "unstaged-A\n" {
		t.Fatalf("staged_only commit should commit staged-A and preserve unstaged-A: committed=%q working=%q", headCommittedA, string(workingA))
	}

	discardBW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/discard?worktree_id=%s", projectID, repoID, idB), nil)
	if discardBW.Code != http.StatusOK {
		t.Fatalf("discard B = %d body=%s", discardBW.Code, discardBW.Body.String())
	}
	workingB, err := os.ReadFile(filepath.Join(siblingB, "shared.txt"))
	testutil.FailErr(t, "read working B", err)
	if string(workingB) != "base\n" {
		t.Fatalf("expected sibling B clean after discard, got %q", string(workingB))
	}
	if gotPrimaryHead := strings.TrimSpace(gittest.Run(t, primaryRoot, "rev-parse", "HEAD")); gotPrimaryHead != primaryHead {
		t.Fatalf("primary HEAD changed unexpectedly: got %s want %s", gotPrimaryHead, primaryHead)
	}

	// 5. Remove target between preview and execution -> 409 worktree_unavailable, zero writes in primary.
	testutil.FailErr(t, "dirty B before remove", os.WriteFile(filepath.Join(siblingB, "shared.txt"), []byte("transient\n"), 0o644))
	previewRemoved := getAuthedJSON[wire.GitDiffView](t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/diff?worktree_id=%s&mode=commit", projectID, repoID, idB), http.StatusOK)
	if !strings.Contains(previewRemoved.Patch, "+transient") {
		t.Fatalf("expected preview patch before removal, got %+v", previewRemoved)
	}
	contractfixture.RunGitIn(t, primaryRoot, "worktree", "remove", "--force", siblingB)

	removedCommitW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/commit?worktree_id=%s", projectID, repoID, idB), map[string]any{
		"message": "must not fall back to primary",
	})
	if removedCommitW.Code != http.StatusConflict {
		t.Fatalf("expected 409 worktree_unavailable after target removal, got %d body=%s", removedCommitW.Code, removedCommitW.Body.String())
	}
	var errResp struct {
		Code wire.ApiErrorCode `json:"code"`
	}
	testutil.FailErr(t, "decode error", json.Unmarshal(removedCommitW.Body.Bytes(), &errResp))
	if errResp.Code != wire.ApiErrorCodeWorktreeUnavailable {
		t.Fatalf("expected worktree_unavailable, got %q", errResp.Code)
	}
	missingStatusReq := contractfixture.NewAuthedRequest(http.MethodGet, fmt.Sprintf("/v1/projects/%s/git/repos/%s/status?worktree_id=00000000-0000-4000-8000-000000009999", projectID, repoID), nil)
	wMissing := httptest.NewRecorder()
	srv.ServeHTTP(wMissing, missingStatusReq)
	if wMissing.Code != http.StatusNotFound {
		t.Fatalf("expected 404 worktree_not_found for unknown UUID, got %d body=%s", wMissing.Code, wMissing.Body.String())
	}
	primaryBytes, err := os.ReadFile(filepath.Join(primaryRoot, "shared.txt"))
	testutil.FailErr(t, "read primary shared.txt", err)
	if string(primaryBytes) != "base\n" || strings.TrimSpace(gittest.Run(t, primaryRoot, "rev-parse", "HEAD")) != primaryHead {
		t.Fatalf("primary checkout mutated after removed-target refusal")
	}
}

func TestGitWorktreeSharedStashRemoteRefsAndBoundBranchProtection(t *testing.T) {
	srv, _, projectID := contractfixture.NewWorktreeTestServer(t)
	p, err := srv.Sources.Workspace.ProjectRegistry.Get(t.Context(), projectID)
	testutil.FailErr(t, "get project", err)
	primaryRoot := gitrepo.CanonicalDir(project.PrimaryRootPath(p))
	repoID := contractfixture.WorktreeRepoID(t, srv, projectID)

	bareRemote := filepath.Join(gitrepo.CanonicalDir(t.TempDir()), "remote.git")
	testutil.FailErr(t, "mkdir bare remote", os.MkdirAll(bareRemote, 0o755))
	contractfixture.RunGitIn(t, bareRemote, "init", "--bare")
	contractfixture.RunGitIn(t, primaryRoot, "remote", "add", "origin", bareRemote)
	primaryBranch, err := git.NewManager().Branch(t.Context(), primaryRoot)
	testutil.FailErr(t, "primary branch", err)
	contractfixture.RunGitIn(t, primaryRoot, "push", "-u", "origin", primaryBranch)

	sibParent := gitrepo.CanonicalDir(t.TempDir())
	sibling := filepath.Join(sibParent, "wt-stash-remote")
	contractfixture.RunGitIn(t, primaryRoot, "worktree", "add", "-b", "feat/shared", sibling)

	wtView := getAuthedJSON[wire.GitWorktreesView](t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/worktrees", projectID, repoID), http.StatusOK)
	var idPrimary, idSibling string
	for _, entry := range wtView.Worktrees {
		switch filepath.Clean(entry.Path) {
		case filepath.Clean(primaryRoot):
			idPrimary = entry.WorktreeID
		case filepath.Clean(sibling):
			idSibling = entry.WorktreeID
		}
	}
	if idPrimary == "" || idSibling == "" {
		t.Fatalf("expected primary and sibling checkout ids, got %+v", wtView)
	}

	// 1. Stash created in sibling is visible in shared repository stashes and can be popped into primary.
	testutil.FailErr(t, "write stash file", os.WriteFile(filepath.Join(sibling, "README.md"), []byte("from-sibling-stash\n"), 0o644))
	stashPushW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/stash?worktree_id=%s", projectID, repoID, idSibling), map[string]any{
		"message": "shared-stash-test",
	})
	if stashPushW.Code != http.StatusOK {
		t.Fatalf("stash push in sibling = %d body=%s", stashPushW.Code, stashPushW.Body.String())
	}
	stashesFromPrimary := getAuthedJSON[wire.GitStashesView](t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/stashes?worktree_id=%s", projectID, repoID, idPrimary), http.StatusOK)
	if len(stashesFromPrimary.Stashes) != 1 || !strings.Contains(stashesFromPrimary.Stashes[0].Subject, "shared-stash-test") {
		t.Fatalf("expected shared stash visible from primary, got %+v", stashesFromPrimary)
	}
	stashPopW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/stash?worktree_id=%s", projectID, repoID, idPrimary), map[string]any{
		"action": "pop",
		"ref":    stashesFromPrimary.Stashes[0].Ref,
	})
	if stashPopW.Code != http.StatusOK {
		t.Fatalf("stash pop in primary = %d body=%s", stashPopW.Code, stashPopW.Body.String())
	}
	poppedBytes, err := os.ReadFile(filepath.Join(primaryRoot, "README.md"))
	testutil.FailErr(t, "read primary README", err)
	if string(poppedBytes) != "from-sibling-stash\n" {
		t.Fatalf("expected popped stash bytes in primary, got %q", string(poppedBytes))
	}
	contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/discard?worktree_id=%s", projectID, repoID, idPrimary), nil)

	// 2. Explicit remote/ref push from sibling and pull into primary.
	testutil.FailErr(t, "write sibling feature", os.WriteFile(filepath.Join(sibling, "feature.txt"), []byte("pushed-from-sibling\n"), 0o644))
	commitSibW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/commit?worktree_id=%s", projectID, repoID, idSibling), map[string]any{
		"message": "feat: sibling commit",
	})
	if commitSibW.Code != http.StatusOK {
		t.Fatalf("commit sibling = %d body=%s", commitSibW.Code, commitSibW.Body.String())
	}
	pushSibW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/push?worktree_id=%s&remote=origin&ref=HEAD:refs/heads/%s", projectID, repoID, idSibling, primaryBranch), nil)
	if pushSibW.Code != http.StatusOK {
		t.Fatalf("push explicit remote/ref from sibling = %d body=%s", pushSibW.Code, pushSibW.Body.String())
	}
	pullPrimW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/pull?worktree_id=%s&remote=origin&ref=%s", projectID, repoID, idPrimary, primaryBranch), nil)
	if pullPrimW.Code != http.StatusOK {
		t.Fatalf("pull explicit remote/ref in primary = %d body=%s", pullPrimW.Code, pullPrimW.Body.String())
	}
	pulledBytes, err := os.ReadFile(filepath.Join(primaryRoot, "feature.txt"))
	testutil.FailErr(t, "read pulled feature.txt", err)
	if string(pulledBytes) != "pushed-from-sibling\n" {
		t.Fatalf("expected pulled feature.txt in primary, got %q", string(pulledBytes))
	}

	// 3. Bound session refuses branch switch on its bound checkout (legacy or explicit), but allows switching an unbound sibling.
	boundSess, _, boundView := contractfixture.CreateBoundSession(t, srv, projectID)
	refusedLegacyW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/checkout?session_id=%s", projectID, repoID, boundSess.ID), map[string]any{
		"branch": "feat/nope",
		"create": true,
	})
	if refusedLegacyW.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 refusing branch switch on bound session legacy target, got %d body=%s", refusedLegacyW.Code, refusedLegacyW.Body.String())
	}
	refusedExplicitBoundW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/checkout?session_id=%s&worktree_id=%s", projectID, repoID, boundSess.ID, boundView.WorktreeID), map[string]any{
		"branch": "feat/nope",
		"create": true,
	})
	if refusedExplicitBoundW.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 refusing branch switch on explicit bound worktree_id, got %d body=%s", refusedExplicitBoundW.Code, refusedExplicitBoundW.Body.String())
	}
	allowedSiblingSwitchW := contractfixture.PostWorktreeJSON(t, srv, fmt.Sprintf("/v1/projects/%s/git/repos/%s/checkout?session_id=%s&worktree_id=%s", projectID, repoID, boundSess.ID, idSibling), map[string]any{
		"branch": "feat/sibling-switched",
		"create": true,
	})
	if allowedSiblingSwitchW.Code != http.StatusOK {
		t.Fatalf("expected 200 switching branch in unbound sibling, got %d body=%s", allowedSiblingSwitchW.Code, allowedSiblingSwitchW.Body.String())
	}
}
