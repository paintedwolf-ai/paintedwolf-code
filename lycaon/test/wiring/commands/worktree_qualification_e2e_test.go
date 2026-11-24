package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

type countingLLMClient struct {
	next  modelcall.LLMClient
	calls atomic.Int64
}

func (c *countingLLMClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.calls.Add(1)
	return c.next.Complete(ctx, req)
}

func (c *countingLLMClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.calls.Add(1)
	return c.next.Stream(ctx, req)
}

func doRaw(t *testing.T, h *wiring.Harness, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		testutil.FailErr(t, "marshal request", err)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, target, reader)
	req.Header.Set("Authorization", "Bearer "+h.APIToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res := httptest.NewRecorder()
	h.Server.ServeHTTP(res, req)
	return res
}

func doJSON[T any](t *testing.T, h *wiring.Harness, method, target string, body any, wantStatus int) T {
	t.Helper()
	res := doRaw(t, h, method, target, body)
	if res.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d; body = %s", method, target, res.Code, wantStatus, res.Body.String())
	}
	var out T
	if res.Body.Len() > 0 {
		testutil.FailErr(t, "decode response", json.Unmarshal(res.Body.Bytes(), &out))
	}
	return out
}

func submitPromptWithApprovals(t *testing.T, s *commandProgramSession, promptText string, answers ...string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := s.h.Sessions.Manager.Submissions.Prompt(t.Context(), s.session, promptText)
		done <- err
	}()
	kind := wire.CheckpointKindToolApproval
	answered := 0
	var runErr error
	testutil.WaitFor(t, "prompt completion", func() bool {
		select {
		case runErr = <-done:
			return true
		default:
		}
		pending, err := s.h.Sessions.Checkpoints.ListPending(t.Context(), s.session, &kind)
		if err != nil || len(pending) == 0 {
			return false
		}
		for _, cp := range pending {
			if answered >= len(answers) {
				t.Fatalf("unexpected approval card: %+v", cp.ToolApproval.Plan)
			}
			opt := answers[answered]
			answered++
			if opt == "recommended" {
				opt = cp.ToolApproval.Plan.RecommendedOptionID
			}
			_, err := s.h.Sessions.Checkpoints.(*hitl.Checkpoints).Authority.ResolveApprovalOption(
				s.h.OwnerCtx(t, t.Context()), s.session, cp.ID, opt,
			)
			testutil.FailErr(t, "resolve approval", err)
		}
		return false
	})
	testutil.FailErr(t, "prompt finished", runErr)
	if answered != len(answers) {
		t.Fatalf("answered %d approvals, want %d", answered, len(answers))
	}
}

func TestWorktreeQualificationNativeAgentAddCrossCheckoutEditAndNextCommandCwd(t *testing.T) {
	admissiontest.Enable(t)
	t.Setenv("LYCAON_SANDBOX", "on")

	models := newSessionModels()
	counted := &countingLLMClient{next: models}
	p := &programHost{h: wiring.BuildForTest(t, wiring.WithLLMClient(counted)), models: models}

	outsideParent := gitrepo.CanonicalDir(outsideCommandFolder(t))
	siblingDir := filepath.Join(outsideParent, "wt-agent")

	turn1Cmd := strings.Join([]string{
		"git worktree add -b feat/agent-sibling '" + siblingDir + "' HEAD",
		"cd '" + siblingDir + "'",
		"printf 'agent sibling unstaged\\n' > notes.txt",
		"printf 'agent sibling staged\\n' > staged_only.txt",
		"git add staged_only.txt",
	}, " && ")
	script := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{
			Pattern:      "turn one create sibling",
			ToolCalls:    []llm.MockToolCall{{ID: "call-turn-1", Name: "command", Args: map[string]any{"command": turn1Cmd}}},
			FollowUpText: "Created sibling worktree.",
		},
		{
			Pattern:      "turn two check cwd",
			ToolCalls:    []llm.MockToolCall{{ID: "call-turn-2", Name: "command", Args: map[string]any{"command": "pwd && cat notes.txt"}}},
			FollowUpText: "Checked primary working directory.",
		},
	}})

	s := p.withClient(t, script, wire.CreateSessionRequest{}, "")
	s.h.SeedProgress(t, t.Context(), s.session)
	gittest.InitCommit(t, s.dir, "seed")
	canonPrimary := gitrepo.CanonicalDir(s.dir)

	var primaryRootID string
	testutil.FailErr(t, "primary root id", s.h.DB.QueryRowContext(
		t.Context(), `SELECT id FROM project_roots WHERE project_id = ? AND is_primary = 1`, s.project,
	).Scan(&primaryRootID))

	repos := doJSON[wire.GitReposView](t, s.h, http.MethodGet, "/v1/projects/"+s.project+"/git/repos", nil, http.StatusOK)
	if len(repos.Repos) != 1 {
		t.Fatalf("repos = %+v", repos.Repos)
	}
	repoID := repos.Repos[0].RepoID

	// Person opens, edits, and saves notes.txt in the primary checkout while viewing primary Git status.
	primaryDoc := doJSON[wire.EditorDocument](t, s.h, http.MethodPost, "/v1/projects/"+s.project+"/editor-documents", wire.OpenEditorDocumentRequest{
		ClientID: "person-window-1",
		RootID:   primaryRootID,
		Path:     "notes.txt",
	}, http.StatusCreated)
	primaryDoc = doJSON[wire.EditorDocument](t, s.h, http.MethodPut, "/v1/projects/"+s.project+"/editor-documents/"+primaryDoc.ID, wire.ReplaceEditorDocumentRequest{
		ClientID:         "person-window-1",
		OperationID:      uuid.NewString(),
		ExpectedRevision: primaryDoc.Revision,
		Content:          "person primary edit\n",
		EOL:              "lf",
	}, http.StatusOK)
	doJSON[wire.EditorDocument](t, s.h, http.MethodPost, "/v1/projects/"+s.project+"/editor-documents/"+primaryDoc.ID+"/save", wire.SaveEditorDocumentRequest{
		ClientID:         "person-window-1",
		OperationID:      uuid.NewString(),
		ExpectedRevision: primaryDoc.Revision,
	}, http.StatusOK)

	// Turn 1: Native agent adds and edits an authorized sibling checkout; zero extra model calls beyond tool + follow-up.
	submitPromptWithApprovals(t, s, "turn one create sibling", "recommended")
	if got := counted.calls.Load(); got != 2 {
		t.Fatalf("model calls after turn 1 = %d, want 2", got)
	}

	// Turn 2: Next independent command runs without cwd override and stays in primary checkout.
	submitPromptWithApprovals(t, s, "turn two check cwd")
	if got := counted.calls.Load(); got != 4 {
		t.Fatalf("model calls after turn 2 = %d, want 4", got)
	}

	var turn2Preview string
	rows, err := s.h.DB.QueryContext(t.Context(), `SELECT o.preview FROM invocation_program_outputs o
		JOIN invocation_programs p ON p.id = o.program_id JOIN invocation_receipts r ON r.id = p.parent_receipt_id
		WHERE r.session_id = ? AND r.tool_call_id = 'call-turn-2' AND o.stream = 'stdout' ORDER BY o.created_at, o.id`, s.session)
	testutil.FailErr(t, "read turn 2 stdout", err)
	for rows.Next() {
		var chunk string
		testutil.FailErr(t, "scan chunk", rows.Scan(&chunk))
		turn2Preview += chunk
	}
	testutil.FailErr(t, "close rows", rows.Close())
	if !strings.Contains(turn2Preview, canonPrimary) || !strings.Contains(turn2Preview, "person primary edit") || strings.Contains(turn2Preview, "agent sibling") {
		t.Fatalf("next command inherited sibling cwd or lost person primary edit: %q", turn2Preview)
	}

	if _, bound, err := s.h.Store.Sessions.GetWorktreeBinding(t.Context(), s.session); err != nil || bound {
		t.Fatalf("agent worktree add unexpectedly bound session: bound=%v err=%v", bound, err)
	}

	wtView := doJSON[wire.GitWorktreesView](t, s.h, http.MethodGet, "/v1/projects/"+s.project+"/git/repos/"+repoID+"/worktrees?session_id="+s.session, nil, http.StatusOK)
	var siblingWT wire.GitWorktreeEntry
	for _, item := range wtView.Worktrees {
		if item.Branch == "feat/agent-sibling" {
			siblingWT = item
		}
	}
	if siblingWT.WorktreeID == "" || siblingWT.Managed || !siblingWT.Available || !siblingWT.CreatedByChat || !siblingWT.UsedByChat {
		t.Fatalf("unexpected sibling inventory entry: %+v (all=%+v)", siblingWT, wtView.Worktrees)
	}

	// Same pathname holds independent staged and unstaged state across checkouts; explicit discard targets only sibling.
	primaryStatus := doJSON[wire.GitStatusSummary](t, s.h, http.MethodGet, "/v1/projects/"+s.project+"/git/repos/"+repoID+"/status", nil, http.StatusOK)
	siblingStatus := doJSON[wire.GitStatusSummary](t, s.h, http.MethodGet, "/v1/projects/"+s.project+"/git/repos/"+repoID+"/status?worktree_id="+siblingWT.WorktreeID, nil, http.StatusOK)
	if siblingStatus.StagedCount != 1 || siblingStatus.UnstagedCount != 1 || primaryStatus.UnstagedCount != 1 {
		t.Fatalf("independent status mismatch: primary=%+v sibling=%+v", primaryStatus, siblingStatus)
	}

	// Explicit discard targeting sibling leaves primary notes.txt untouched.
	discardRes := doRaw(t, s.h, http.MethodPost, "/v1/projects/"+s.project+"/git/repos/"+repoID+"/discard?worktree_id="+siblingWT.WorktreeID, map[string]any{})
	if discardRes.Code != http.StatusNoContent && discardRes.Code != http.StatusOK {
		t.Fatalf("sibling discard = %d %s", discardRes.Code, discardRes.Body.String())
	}
	primaryBytes, err := os.ReadFile(filepath.Join(canonPrimary, "notes.txt"))
	testutil.FailErr(t, "read primary notes.txt", err)
	if string(primaryBytes) != "person primary edit\n" {
		t.Fatalf("sibling discard mutated primary checkout: %q", primaryBytes)
	}
}

func TestWorktreeQualificationSaveBarrierExternalContinuityAndLifecycle(t *testing.T) {
	testDir := gitrepo.CanonicalDir(t.TempDir())
	baseDir := gitrepo.CanonicalDir(t.TempDir())
	primaryDir := filepath.Join(baseDir, "primary")
	partialSiblingDir := filepath.Join(baseDir, "wt-partial")
	extShellDir := filepath.Join(baseDir, "wt-shell")
	extMovedDir := filepath.Join(baseDir, "wt-shell-moved")
	barrierSiblingDir := filepath.Join(baseDir, "wt-save-barrier")
	staleExternalDir := filepath.Join(baseDir, "wt-stale-external")

	testutil.FailErr(t, "mkdir primary", os.MkdirAll(filepath.Join(primaryDir, "packages", "ui"), 0o755))
	testutil.FailErr(t, "seed root file", os.WriteFile(filepath.Join(primaryDir, "notes.txt"), []byte("root v1\n"), 0o644))
	testutil.FailErr(t, "seed ui file", os.WriteFile(filepath.Join(primaryDir, "packages", "ui", "view.ts"), []byte("export const v = 1;\n"), 0o644))
	gittest.InitCommit(t, primaryDir, "initial")

	gittest.Run(t, primaryDir, "worktree", "add", "-b", "feat/partial", partialSiblingDir, "HEAD")
	gittest.Run(t, primaryDir, "worktree", "add", "-b", "feat/ext-shell", extShellDir, "HEAD")
	gittest.Run(t, primaryDir, "worktree", "add", "-b", "feat/save-barrier", barrierSiblingDir, "HEAD")
	gittest.Run(t, primaryDir, "worktree", "add", "-b", "feat/stale-ext", staleExternalDir, "HEAD")
	testutil.FailErr(t, "remove stale external dir", os.RemoveAll(staleExternalDir))

	// Seed a nested unrelated repository and a directory with a fake reverse .git pointer.
	nestedUnrelated := filepath.Join(primaryDir, "vendor-unrelated")
	testutil.FailErr(t, "mkdir nested unrelated", os.MkdirAll(nestedUnrelated, 0o755))
	testutil.FailErr(t, "seed nested file", os.WriteFile(filepath.Join(nestedUnrelated, "README.md"), []byte("unrelated\n"), 0o644))
	gittest.InitCommit(t, nestedUnrelated, "unrelated init")

	fakeReverseDir := filepath.Join(baseDir, "wt-fake-pointer")
	testutil.FailErr(t, "mkdir fake reverse", os.MkdirAll(fakeReverseDir, 0o755))
	testutil.FailErr(t, "write fake .git pointer", os.WriteFile(
		filepath.Join(fakeReverseDir, ".git"),
		[]byte("gitdir: "+filepath.Join(primaryDir, ".git", "worktrees", "nonexistent")+"\n"),
		0o644,
	))

	var armSaveReplace atomic.Bool
	var projID, repoID, primaryRootID, extShellID, barrierDocID string

	t.Run("phase1_admission_barrier_and_move", func(t *testing.T) {
		h := wiring.BuildForTestAt(t, testDir, wiring.WithStoreCommitObserver(func(writes []db.TableWrite) {
			for _, w := range writes {
				if w.Table == "editor_mutations" && w.Op == "insert" && armSaveReplace.CompareAndSwap(true, false) {
					_ = os.RemoveAll(barrierSiblingDir)
					_ = os.MkdirAll(barrierSiblingDir, 0o755)
					_ = os.WriteFile(filepath.Join(barrierSiblingDir, "notes.txt"), []byte("replacement bystander\n"), 0o644)
				}
			}
		}))

		proj, err := h.Server.Sources.Workspace.ProjectRegistry.Create(t.Context(), project.CreateParams{
			Roots: []project.AttachRootParams{
				{Path: primaryDir, Label: "primary"},
				{Path: filepath.Join(partialSiblingDir, "packages", "ui"), Label: "ui-slice"},
				{Path: barrierSiblingDir, Label: "barrier-wt"},
			},
		})
		testutil.FailErr(t, "create project", err)
		projID = proj.ID
		primaryRootID = proj.Roots[0].ID

		repos := doJSON[wire.GitReposView](t, h, http.MethodGet, "/v1/projects/"+projID+"/git/repos", nil, http.StatusOK)
		for _, r := range repos.Repos {
			if len(r.RootIDs) > 0 && r.RootIDs[0] == primaryRootID {
				repoID = r.RepoID
			}
		}
		if repoID == "" {
			t.Fatalf("missing primary repo in %+v", repos.Repos)
		}

		wtView := doJSON[wire.GitWorktreesView](t, h, http.MethodGet, "/v1/projects/"+projID+"/git/repos/"+repoID+"/worktrees", nil, http.StatusOK)
		var partialWT, barrierWT wire.GitWorktreeEntry
		for _, entry := range wtView.Worktrees {
			if strings.Contains(entry.Path, "wt-fake-pointer") || strings.Contains(entry.Path, "vendor-unrelated") {
				t.Fatalf("unrelated or fake checkout adopted into inventory: %+v", entry)
			}
			switch entry.Branch {
			case "feat/partial":
				partialWT = entry
			case "feat/ext-shell":
				extShellID = entry.WorktreeID
				if entry.UsedByChat || entry.CreatedByChat {
					t.Fatalf("external shell checkout invented actor attribution: %+v", entry)
				}
			case "feat/save-barrier":
				barrierWT = entry
			}
		}
		if partialWT.WorktreeID == "" || !partialWT.Available || extShellID == "" || barrierWT.WorktreeID == "" {
			t.Fatalf("missing expected checkouts in %+v", wtView.Worktrees)
		}

		// Partial root grants packages/ui/view.ts in partialWT, while refusing root-level notes.txt outside the slice.
		uiRootID := proj.Roots[1].ID
		doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+projID+"/editor-documents?worktree_id="+partialWT.WorktreeID, wire.OpenEditorDocumentRequest{
			ClientID: "win-1",
			RootID:   uiRootID,
			Path:     "view.ts",
		}, http.StatusCreated)
		unauthRes := doRaw(t, h, http.MethodPost, "/v1/projects/"+projID+"/editor-documents?worktree_id="+partialWT.WorktreeID, wire.OpenEditorDocumentRequest{
			ClientID: "win-1",
			RootID:   primaryRootID,
			Path:     "notes.txt",
		})
		if unauthRes.Code == http.StatusOK || unauthRes.Code == http.StatusCreated {
			t.Fatalf("partial root unexpectedly allowed ungranted parent file in sibling checkout")
		}

		// Open barrierSiblingDir notes.txt in two windows, edit, and replace directory during pending save commit.
		docWin1 := doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+projID+"/editor-documents?worktree_id="+barrierWT.WorktreeID, wire.OpenEditorDocumentRequest{
			ClientID: "win-1",
			RootID:   primaryRootID,
			Path:     "notes.txt",
		}, http.StatusCreated)
		barrierDocID = docWin1.ID
		doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+projID+"/editor-documents?worktree_id="+barrierWT.WorktreeID, wire.OpenEditorDocumentRequest{
			ClientID: "win-2",
			RootID:   primaryRootID,
			Path:     "notes.txt",
		}, http.StatusCreated)
		docWin1 = doJSON[wire.EditorDocument](t, h, http.MethodPut, "/v1/projects/"+projID+"/editor-documents/"+barrierDocID, wire.ReplaceEditorDocumentRequest{
			ClientID:         "win-1",
			OperationID:      uuid.NewString(),
			ExpectedRevision: docWin1.Revision,
			Content:          "two-window pending save\n",
			EOL:              "lf",
		}, http.StatusOK)

		armSaveReplace.Store(true)
		saveRes := doRaw(t, h, http.MethodPost, "/v1/projects/"+projID+"/editor-documents/"+barrierDocID+"/save", wire.SaveEditorDocumentRequest{
			ClientID:         "win-1",
			OperationID:      uuid.NewString(),
			ExpectedRevision: docWin1.Revision,
		})
		if saveRes.Code == http.StatusOK {
			t.Fatalf("save unexpectedly succeeded into replacement directory")
		}
		bystanderBytes, err := os.ReadFile(filepath.Join(barrierSiblingDir, "notes.txt"))
		testutil.FailErr(t, "read replacement bystander", err)
		if string(bystanderBytes) != "replacement bystander\n" {
			t.Fatalf("pending save overwrote replacement incarnation: %q", bystanderBytes)
		}

		// Branch switch, detach, and git worktree move before host restart.
		gittest.Run(t, extShellDir, "checkout", "-b", "feat/ext-switched")
		gittest.Run(t, extShellDir, "checkout", "--detach")
		gittest.Run(t, primaryDir, "worktree", "move", extShellDir, extMovedDir)
	})

	t.Run("phase2_restart_continuity_recovery_and_lifecycle", func(t *testing.T) {
		h := wiring.BuildForTestAt(t, testDir)

		// Proven move across restart preserves WorktreeID; replacement at same path gets fresh ID.
		wtAfterMove := doJSON[wire.GitWorktreesView](t, h, http.MethodGet, "/v1/projects/"+projID+"/git/repos/"+repoID+"/worktrees", nil, http.StatusOK)
		foundMoved := false
		for _, entry := range wtAfterMove.Worktrees {
			if gitrepo.CanonicalDir(entry.Path) == gitrepo.CanonicalDir(extMovedDir) {
				foundMoved = true
				if entry.WorktreeID != extShellID || !entry.Detached {
					t.Fatalf("moved detached checkout lost identity across restart: got=%+v wantID=%s", entry, extShellID)
				}
			}
		}
		if !foundMoved {
			t.Fatalf("moved checkout missing after restart: %+v", wtAfterMove.Worktrees)
		}

		gittest.Run(t, primaryDir, "worktree", "remove", "--force", extMovedDir)
		gittest.Run(t, primaryDir, "worktree", "add", "-b", "feat/recreated-same-path", extMovedDir, "HEAD")
		wtRecreated := doJSON[wire.GitWorktreesView](t, h, http.MethodGet, "/v1/projects/"+projID+"/git/repos/"+repoID+"/worktrees", nil, http.StatusOK)
		for _, entry := range wtRecreated.Worktrees {
			if gitrepo.CanonicalDir(entry.Path) == gitrepo.CanonicalDir(extMovedDir) && entry.WorktreeID == extShellID {
				t.Fatalf("replacement checkout reused retired WorktreeID %s", extShellID)
			}
		}
		staleStatus := doRaw(t, h, http.MethodGet, "/v1/projects/"+projID+"/git/repos/"+repoID+"/status?worktree_id="+extShellID, nil)
		if staleStatus.Code != http.StatusConflict {
			t.Fatalf("stale worktree_id status = %d, want 409", staleStatus.Code)
		}

		// Reopen orphaned draft by document_id after restart and save its recovered contents into primary.
		recovered := doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+projID+"/editor-documents", wire.OpenEditorDocumentRequest{
			ClientID:   "win-2-reopen",
			DocumentID: barrierDocID,
			RootID:     primaryRootID,
			Path:       "notes.txt",
		}, http.StatusOK)
		if !recovered.Dirty || !recovered.CheckoutUnavailable {
			t.Fatalf("expected dirty unavailable recovered draft, got %+v", recovered)
		}
		primarySaveTarget := doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+projID+"/editor-documents", wire.OpenEditorDocumentRequest{
			ClientID: "win-2-reopen",
			RootID:   primaryRootID,
			Path:     "notes.txt",
		}, http.StatusCreated)
		primarySaveTarget = doJSON[wire.EditorDocument](t, h, http.MethodPut, "/v1/projects/"+projID+"/editor-documents/"+primarySaveTarget.ID, wire.ReplaceEditorDocumentRequest{
			ClientID:         "win-2-reopen",
			OperationID:      uuid.NewString(),
			ExpectedRevision: primarySaveTarget.Revision,
			Content:          "two-window pending save\n",
			EOL:              "lf",
		}, http.StatusOK)
		doJSON[wire.EditorDocument](t, h, http.MethodPost, "/v1/projects/"+projID+"/editor-documents/"+primarySaveTarget.ID+"/save", wire.SaveEditorDocumentRequest{
			ClientID:         "win-2-reopen",
			OperationID:      uuid.NewString(),
			ExpectedRevision: primarySaveTarget.Revision,
		}, http.StatusOK)

		// Managed bind and return-and-remove while inspecting another checkout preserve unrelated stale registrations.
		discovered, ok := gitrepo.Discover(primaryDir)
		if !ok {
			t.Fatal("discover primary repo")
		}
		staleAdminGitdir := filepath.Join(discovered.CommonDir, "worktrees", "wt-stale-external", "gitdir")
		if _, err := os.Stat(staleAdminGitdir); err != nil {
			t.Fatalf("expected unrelated stale registration before bind/remove: %v", err)
		}

		sess, err := h.Store.Sessions.Create(t.Context(), wire.CreateSessionRequest{ProjectID: projID}, projID)
		testutil.FailErr(t, "create session", err)
		bindView := doJSON[wire.GitWorktreeView](t, h, http.MethodPut, "/v1/sessions/"+sess.ID+"/git-worktree", wire.GitWorktreeBindRequest{
			RepoID: repoID,
			Branch: "session/qual-managed",
		}, http.StatusOK)
		if !bindView.Bound || bindView.WorktreeID == "" {
			t.Fatalf("bind view = %+v", bindView)
		}
		binding, _, err := h.Store.Sessions.GetWorktreeBinding(t.Context(), sess.ID)
		testutil.FailErr(t, "get binding", err)

		testutil.FailErr(t, "dirty managed checkout", os.WriteFile(filepath.Join(binding.WorktreePath, "notes.txt"), []byte("dirty\n"), 0o644))
		dirtyDel := doRaw(t, h, http.MethodDelete, "/v1/sessions/"+sess.ID+"/git-worktree", nil)
		if dirtyDel.Code != http.StatusConflict {
			t.Fatalf("dirty remove = %d, want 409", dirtyDel.Code)
		}
		testutil.FailErr(t, "clean managed checkout", os.WriteFile(filepath.Join(binding.WorktreePath, "notes.txt"), []byte("root v1\n"), 0o644))
		cleanDel := doRaw(t, h, http.MethodDelete, "/v1/sessions/"+sess.ID+"/git-worktree", nil)
		if cleanDel.Code != http.StatusNoContent {
			t.Fatalf("clean remove = %d %s", cleanDel.Code, cleanDel.Body.String())
		}
		if _, err := os.Stat(binding.WorktreePath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed checkout dir still exists: %v", err)
		}
		if _, err := os.Stat(staleAdminGitdir); err != nil {
			t.Fatalf("unrelated stale registration was pruned: %v", err)
		}
	})
}
