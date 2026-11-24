package sourcecontracts

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	hostapi "github.com/lycaon/lycaon/internal/api"
	contractfixture "github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/project/projecttest"
	sessionstore "github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test User",
		"GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test User",
		"GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
}

func callJSON[T any](t *testing.T, srv *hostapi.Server, method, target string, body any, wantStatus int) T {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		testutil.FailErr(t, "encode request", err)
		reader = bytes.NewReader(encoded)
	}
	req := contractfixture.NewAuthedRequest(method, target, reader)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != wantStatus {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, target, res.Code, wantStatus, res.Body.String())
	}
	var out T
	testutil.FailErr(t, "decode response", json.Unmarshal(res.Body.Bytes(), &out))
	return out
}

func TestWorktreeEditorSameNamedFilesSwitchRestartAndReverseSave(t *testing.T) {
	baseDir := gitrepo.CanonicalDir(t.TempDir())
	repoDir := filepath.Join(baseDir, "primary")
	siblingDir := filepath.Join(baseDir, "sibling")
	docsDir := filepath.Join(baseDir, "docs")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(filepath.Join(repoDir, "pkg"), 0o755))
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(filepath.Join(repoDir, "sub"), 0o755))
	testutil.FailErr(t, "mkdir docs", os.MkdirAll(docsDir, 0o755))

	runGitCmd(t, repoDir, "init", "-b", "main")
	testutil.FailErr(t, "write app.go", os.WriteFile(filepath.Join(repoDir, "pkg", "app.go"), []byte("package pkg // main v1\n"), 0o644))
	testutil.FailErr(t, "write config.toml", os.WriteFile(filepath.Join(repoDir, "sub", "config.toml"), []byte("mode = \"main\"\n"), 0o644))
	testutil.FailErr(t, "write guide.md", os.WriteFile(filepath.Join(docsDir, "guide.md"), []byte("# Guide\n"), 0o644))
	runGitCmd(t, repoDir, "add", ".")
	runGitCmd(t, repoDir, "commit", "-m", "initial")

	// Create a detached-HEAD linked worktree with distinct initial contents.
	runGitCmd(t, repoDir, "worktree", "add", "--detach", siblingDir, "HEAD")
	testutil.FailErr(t, "write sibling app.go", os.WriteFile(filepath.Join(siblingDir, "pkg", "app.go"), []byte("package pkg // sibling v1\n"), 0o644))
	testutil.FailErr(t, "write sibling config.toml", os.WriteFile(filepath.Join(siblingDir, "sub", "config.toml"), []byte("mode = \"sibling\"\n"), 0o644))

	ledger, database, withLedger := contractfixture.TestSourceLedger(t)
	docStore := editordoc.NewStore(database)
	var docs *editordoc.Service
	srv := contractfixture.NewTestServer(t, withLedger, contractfixture.WithSessionStore(sessionstore.NewSQL(database)), func(d *hostapi.Dependencies) {
		docs = editordoc.New(docStore, ledger, ledger.History, d.Core.Projects)
		d.Source.EditorDocuments = docs
	})
	t.Cleanup(func() { _ = docs.Close(context.Background()) })

	proj, err := srv.Sources.Workspace.ProjectRegistry.Create(t.Context(), project.CreateParams{
		Roots: []project.AttachRootParams{
			{Path: repoDir, Label: "repo"},
			{Path: filepath.Join(repoDir, "sub"), Label: "sub"},
			{Path: docsDir, Label: "docs"},
		},
	})
	testutil.FailErr(t, "create project", err)
	primaryRootID := proj.Roots[0].ID
	subRoot := proj.Roots[1]
	unrelatedRoot := proj.Roots[2]

	// Discover repositories and checkouts through the Git worktrees API.
	reposView := callJSON[wire.GitReposView](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/git/repos", nil, http.StatusOK)
	if len(reposView.Repos) != 1 {
		t.Fatalf("repos count = %d, want 1", len(reposView.Repos))
	}
	repoID := reposView.Repos[0].RepoID
	wtView := callJSON[wire.GitWorktreesView](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/git/repos/"+repoID+"/worktrees", nil, http.StatusOK)
	if len(wtView.Worktrees) != 2 {
		t.Fatalf("worktrees count = %d, want 2", len(wtView.Worktrees))
	}
	var primaryWT, siblingWT wire.GitWorktreeEntry
	for _, entry := range wtView.Worktrees {
		if entry.IsProjectCheckout {
			primaryWT = entry
		} else {
			siblingWT = entry
		}
	}
	if primaryWT.WorktreeID == "" || siblingWT.WorktreeID == "" || !siblingWT.Detached {
		t.Fatalf("unexpected worktree entries: primary=%+v sibling=%+v", primaryWT, siblingWT)
	}

	// Verify mixed roots & subdirectory projection on GET /source/workspace.
	wsPrimary := callJSON[wire.SourceWorkspace](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/source/workspace", nil, http.StatusOK)
	wsSibling := callJSON[wire.SourceWorkspace](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/source/workspace?worktree_id="+url.QueryEscape(siblingWT.WorktreeID), nil, http.StatusOK)
	if wsPrimary.WorkspaceID == wsSibling.WorkspaceID {
		t.Fatalf("primary and sibling workspaces collided: %s", wsPrimary.WorkspaceID)
	}
	byRoot := make(map[string]wire.SourceWorkspaceRoot, len(wsSibling.Roots))
	for _, r := range wsSibling.Roots {
		byRoot[r.ID] = r
	}
	if byRoot[primaryRootID].Path != siblingDir || byRoot[primaryRootID].BranchID != "worktree:"+siblingWT.WorktreeID || byRoot[primaryRootID].WorktreeID != siblingWT.WorktreeID {
		t.Fatalf("primary root not projected onto sibling checkout: %+v", byRoot[primaryRootID])
	}
	if byRoot[subRoot.ID].Path != filepath.Join(siblingDir, "sub") || byRoot[subRoot.ID].BranchID != "worktree:"+siblingWT.WorktreeID {
		t.Fatalf("subdirectory root not projected onto sibling sub-path: %+v", byRoot[subRoot.ID])
	}
	if byRoot[unrelatedRoot.ID].Path != docsDir || byRoot[unrelatedRoot.ID].BranchID != "" {
		t.Fatalf("unrelated root mutated during repo checkout projection: %+v", byRoot[unrelatedRoot.ID])
	}

	// Conflicting worktree_id values for the same repository must reject with 400.
	conflictReq := contractfixture.NewAuthedRequest(
		http.MethodGet,
		"/v1/projects/"+proj.ID+"/source/workspace?worktree_id="+primaryWT.WorktreeID+"&worktree_id="+siblingWT.WorktreeID,
		nil,
	)
	conflictRes := httptest.NewRecorder()
	srv.ServeHTTP(conflictRes, conflictReq)
	if conflictRes.Code != http.StatusBadRequest {
		t.Fatalf("conflicting same-repo worktree_ids status = %d, want 400", conflictRes.Code)
	}

	// Subdirectory root read on sibling checkout returns sibling sub/config.toml.
	subRead := callJSON[wire.ProjectSourceReadResponse](
		t, srv, http.MethodGet,
		"/v1/projects/"+proj.ID+"/source?root_id="+url.QueryEscape(subRoot.ID)+"&path=config.toml&worktree_id="+url.QueryEscape(siblingWT.WorktreeID),
		nil, http.StatusOK,
	)
	if subRead.Content != "mode = \"sibling\"\n" {
		t.Fatalf("subdirectory sibling read = %q", subRead.Content)
	}

	// Open same-named pkg/app.go in both primary and sibling checkouts.
	docBaseURL := "/v1/projects/" + proj.ID + "/editor-documents"
	primaryDoc := callJSON[wire.EditorDocument](t, srv, http.MethodPost, docBaseURL, wire.OpenEditorDocumentRequest{
		RootID: primaryRootID, Path: "pkg/app.go", ClientID: "win-1",
	}, http.StatusCreated)
	siblingDoc := callJSON[wire.EditorDocument](t, srv, http.MethodPost, docBaseURL+"?worktree_id="+url.QueryEscape(siblingWT.WorktreeID), wire.OpenEditorDocumentRequest{
		RootID: primaryRootID, Path: "pkg/app.go", ClientID: "win-1",
	}, http.StatusCreated)
	if primaryDoc.ID == siblingDoc.ID || primaryDoc.FileID == siblingDoc.FileID || primaryDoc.WorkspaceID == siblingDoc.WorkspaceID {
		t.Fatalf("same-named files across checkouts shared identity: primary=%+v sibling=%+v", primaryDoc, siblingDoc)
	}
	if primaryDoc.BranchID != "" || siblingDoc.BranchID != "worktree:"+siblingWT.WorktreeID {
		t.Fatalf("unexpected branch_ids: primary=%q sibling=%q", primaryDoc.BranchID, siblingDoc.BranchID)
	}

	// Edit both drafts and switch workspace reads repeatedly between checkouts.
	primaryEdited := "package pkg // main edited\n"
	siblingEdited := "package pkg // sibling edited\n"
	primaryDoc = callJSON[wire.EditorDocument](t, srv, http.MethodPut, docBaseURL+"/"+primaryDoc.ID, wire.ReplaceEditorDocumentRequest{
		OperationID: uuid.NewString(), ClientID: "win-1", ExpectedRevision: primaryDoc.Revision, Content: primaryEdited, EOL: "lf",
	}, http.StatusOK)
	_ = callJSON[wire.SourceWorkspace](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/source/workspace?worktree_id="+url.QueryEscape(siblingWT.WorktreeID), nil, http.StatusOK)
	siblingDoc = callJSON[wire.EditorDocument](t, srv, http.MethodPut, docBaseURL+"/"+siblingDoc.ID+"?worktree_id="+url.QueryEscape(siblingWT.WorktreeID), wire.ReplaceEditorDocumentRequest{
		OperationID: uuid.NewString(), ClientID: "win-1", ExpectedRevision: siblingDoc.Revision, Content: siblingEdited, EOL: "lf",
	}, http.StatusOK)
	_ = callJSON[wire.SourceWorkspace](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/source/workspace?worktree_id="+url.QueryEscape(primaryWT.WorktreeID), nil, http.StatusOK)

	// Simulate host restart: recover persisted documents and reopen both by document_id.
	testutil.FailErr(t, "recover editor documents", docs.Recover(t.Context()))
	reopenedPrimary := callJSON[wire.EditorDocument](t, srv, http.MethodPost, docBaseURL, wire.OpenEditorDocumentRequest{
		DocumentID: primaryDoc.ID, ClientID: "win-restarted",
	}, http.StatusCreated)
	reopenedSibling := callJSON[wire.EditorDocument](t, srv, http.MethodPost, docBaseURL, wire.OpenEditorDocumentRequest{
		DocumentID: siblingDoc.ID, ClientID: "win-restarted",
	}, http.StatusCreated)
	if !reopenedPrimary.Dirty || !reopenedSibling.Dirty || reopenedSibling.BranchID != "worktree:"+siblingWT.WorktreeID {
		t.Fatalf("reopened documents lost dirty state or branch binding: primary=%+v sibling=%+v", reopenedPrimary, reopenedSibling)
	}

	// Save in reverse order (sibling first while primary workspace is active, then primary).
	savedSibling := callJSON[wire.EditorDocument](t, srv, http.MethodPost, docBaseURL+"/"+siblingDoc.ID+"/save", wire.SaveEditorDocumentRequest{
		OperationID: uuid.NewString(), ClientID: "win-restarted", ExpectedRevision: reopenedSibling.Revision,
	}, http.StatusOK)
	savedPrimary := callJSON[wire.EditorDocument](t, srv, http.MethodPost, docBaseURL+"/"+primaryDoc.ID+"/save", wire.SaveEditorDocumentRequest{
		OperationID: uuid.NewString(), ClientID: "win-restarted", ExpectedRevision: reopenedPrimary.Revision,
	}, http.StatusOK)
	if savedSibling.Dirty || savedPrimary.Dirty {
		t.Fatalf("saved documents remained dirty: primary=%v sibling=%v", savedPrimary.Dirty, savedSibling.Dirty)
	}

	wantFiles(t, repoDir, map[string]string{"pkg/app.go": primaryEdited})
	wantFiles(t, siblingDir, map[string]string{"pkg/app.go": siblingEdited})

	// Both checkouts must hold independent saved history in sourceledger.
	primaryHead, err := ledger.History.ResolveHeadByFile(t.Context(), proj.ID, sourcebranch.Trunk, savedPrimary.FileID)
	testutil.FailErr(t, "resolve primary ledger head", err)
	siblingHead, err := ledger.History.ResolveHeadByFile(t.Context(), proj.ID, sourcebranch.ForWorktree(siblingWT.WorktreeID), savedSibling.FileID)
	testutil.FailErr(t, "resolve sibling ledger head", err)
	if primaryHead.VersionID == "" || siblingHead.VersionID == "" || primaryHead.VersionID == siblingHead.VersionID {
		t.Fatalf("expected distinct ledger versions: primary=%+v sibling=%+v", primaryHead, siblingHead)
	}
}

func TestWorktreeEditorSaveRacesRemovalAndPathReuse(t *testing.T) {
	baseDir := gitrepo.CanonicalDir(t.TempDir())
	repoDir := filepath.Join(baseDir, "primary")
	siblingDir := filepath.Join(baseDir, "sibling")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(filepath.Join(repoDir, "pkg"), 0o755))

	runGitCmd(t, repoDir, "init", "-b", "main")
	testutil.FailErr(t, "write app.go", os.WriteFile(filepath.Join(repoDir, "pkg", "app.go"), []byte("package pkg // base\n"), 0o644))
	runGitCmd(t, repoDir, "add", ".")
	runGitCmd(t, repoDir, "commit", "-m", "initial")
	runGitCmd(t, repoDir, "worktree", "add", "-b", "feat/race", siblingDir, "HEAD")

	ledger, database, withLedger := contractfixture.TestSourceLedger(t)
	docStore := editordoc.NewStore(database)
	var docs *editordoc.Service
	srv := contractfixture.NewTestServer(t, withLedger, contractfixture.WithSessionStore(sessionstore.NewSQL(database)), func(d *hostapi.Dependencies) {
		docs = editordoc.New(docStore, ledger, ledger.History, d.Core.Projects)
		d.Source.EditorDocuments = docs
	})
	t.Cleanup(func() { _ = docs.Close(context.Background()) })

	proj, err := projecttest.CreateWithRoot(t.Context(), srv.Sources.Workspace.ProjectRegistry, repoDir)
	testutil.FailErr(t, "create project", err)
	primaryRootID := proj.Roots[0].ID

	reposView := callJSON[wire.GitReposView](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/git/repos", nil, http.StatusOK)
	repoID := reposView.Repos[0].RepoID
	wtView := callJSON[wire.GitWorktreesView](t, srv, http.MethodGet, "/v1/projects/"+proj.ID+"/git/repos/"+repoID+"/worktrees", nil, http.StatusOK)
	var siblingID string
	for _, entry := range wtView.Worktrees {
		if !entry.IsProjectCheckout {
			siblingID = entry.WorktreeID
		}
	}

	docBaseURL := "/v1/projects/" + proj.ID + "/editor-documents"
	siblingDoc := callJSON[wire.EditorDocument](t, srv, http.MethodPost, docBaseURL+"?worktree_id="+url.QueryEscape(siblingID), wire.OpenEditorDocumentRequest{
		RootID: primaryRootID, Path: "pkg/app.go", ClientID: "win-race",
	}, http.StatusCreated)
	unsavedDraft := "package pkg // unsaved draft before removal\n"
	siblingDoc = callJSON[wire.EditorDocument](t, srv, http.MethodPut, docBaseURL+"/"+siblingDoc.ID, wire.ReplaceEditorDocumentRequest{
		OperationID: uuid.NewString(), ClientID: "win-race", ExpectedRevision: siblingDoc.Revision, Content: unsavedDraft, EOL: "lf",
	}, http.StatusOK)

	// Deterministically remove the linked worktree and reuse its exact path right before disk write.
	replacementContent := "package pkg // unrelated replacement directory\n"
	docs.SetBeforePublish(func(context.Context, *editordoc.Document, *editordoc.Mutation) {
		runGitCmd(t, repoDir, "worktree", "remove", "--force", siblingDir)
		_ = os.RemoveAll(siblingDir)
		if mkErr := os.MkdirAll(filepath.Join(siblingDir, "pkg"), 0o755); mkErr != nil {
			t.Errorf("mkdir replacement: %v", mkErr)
			return
		}
		if wErr := os.WriteFile(filepath.Join(siblingDir, "pkg", "app.go"), []byte(replacementContent), 0o644); wErr != nil {
			t.Errorf("write replacement: %v", wErr)
		}
	})
	defer docs.SetBeforePublish(nil)

	saveBody, err := json.Marshal(wire.SaveEditorDocumentRequest{
		OperationID: uuid.NewString(), ClientID: "win-race", ExpectedRevision: siblingDoc.Revision,
	})
	testutil.FailErr(t, "marshal save request", err)
	saveReq := contractfixture.NewAuthedRequest(http.MethodPost, docBaseURL+"/"+siblingDoc.ID+"/save", bytes.NewReader(saveBody))
	saveRes := httptest.NewRecorder()
	srv.ServeHTTP(saveRes, saveReq)
	if saveRes.Code != http.StatusConflict {
		t.Fatalf("save after checkout removal + path reuse status = %d, want 409: %s", saveRes.Code, saveRes.Body.String())
	}
	var apiErr wire.ErrorResponse
	testutil.FailErr(t, "decode error", json.Unmarshal(saveRes.Body.Bytes(), &apiErr))
	if apiErr.Code != wire.ApiErrorCodeWorktreeUnavailable {
		t.Fatalf("error code = %q, want %q", apiErr.Code, wire.ApiErrorCodeWorktreeUnavailable)
	}

	// Replacement directory at the reused path must remain completely untouched!
	wantFiles(t, siblingDir, map[string]string{"pkg/app.go": replacementContent})

	// Simulate restart: Recover settles any pending mutation without losing the unsaved draft,
	// and reopening by document_id marks CheckoutUnavailable while preserving the draft.
	testutil.FailErr(t, "recover after failed save", docs.Recover(t.Context()))
	reopened := callJSON[wire.EditorDocument](t, srv, http.MethodPost, docBaseURL, wire.OpenEditorDocumentRequest{
		DocumentID: siblingDoc.ID, ClientID: "win-after-restart",
	}, http.StatusCreated)
	if !reopened.CheckoutUnavailable || !reopened.Dirty || !reopened.Absent {
		t.Fatalf("reopened removed-checkout document flags: unavailable=%v dirty=%v absent=%v", reopened.CheckoutUnavailable, reopened.Dirty, reopened.Absent)
	}
	stored, err := docStore.Get(t.Context(), siblingDoc.ID)
	testutil.FailErr(t, "read stored document", err)
	if stored.Draft != unsavedDraft {
		t.Fatalf("stored draft = %q, want %q", stored.Draft, unsavedDraft)
	}

	// Explicit Save As to the live primary checkout succeeds and writes the preserved draft.
	primaryRead := callJSON[wire.ProjectSourceReadResponse](
		t, srv, http.MethodGet,
		"/v1/projects/"+proj.ID+"/source?root_id="+url.QueryEscape(primaryRootID)+"&path=pkg/app.go",
		nil, http.StatusOK,
	)
	_ = callJSON[wire.ProjectSourceWriteResponse](t, srv, http.MethodPut, "/v1/projects/"+proj.ID+"/source", wire.PutProjectSourceRequest{
		OperationID: uuid.NewString(),
		RootID:      primaryRootID,
		Path:        "pkg/app.go",
		Content:     stored.Draft,
		Encoding:    wire.SourceEncodingUTF8,
		BaseSHA256:  primaryRead.SHA256,
	}, http.StatusOK)
	wantFiles(t, repoDir, map[string]string{"pkg/app.go": unsavedDraft})
}
