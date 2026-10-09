//go:build integration

package inputs_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	session "github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestAskUserArtifactReviewAttach(t *testing.T) {
	fx := setupAskUserIntegration(t)
	ctx := context.Background()
	store := visual.NewMemoryStore()
	fx.wfMgr.SetVisualArtifacts(store, func(_ context.Context, sessionID string) string {
		return sessionID
	})
	run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	art, err := store.Put(ctx, fx.sess.ID, visual.Entry{
		Meta:  wire.VisualArtifact{Mime: "image/png", Source: wire.VisualArtifactSourceCapture, EvidenceHandle: "page#1"},
		Bytes: visual.TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "Put artifact", err)

	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt":    "Approve this layout?",
		"artifacts": []any{art.ID},
	}, tools.ToolContext{SessionID: fx.sess.ID, Agent: orchestration.ProfileCoordinator})
	testutil.FailErr(t, "ask_user", err)
	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	phaseID, _ := body["phase_id"].(string)

	msgs, err := fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	var found bool
	for _, m := range msgs {
		if m.WorkflowFeedback != nil && m.WorkflowFeedback.PhaseID == phaseID {
			found = true
			if m.WorkflowFeedback.ArtifactID != art.ID {
				t.Fatalf("meta artifact = %q", m.WorkflowFeedback.ArtifactID)
			}
			if m.WorkflowFeedback.Purpose != "review" {
				t.Fatalf("purpose = %q", m.WorkflowFeedback.Purpose)
			}
			if len(m.WorkflowFeedback.Options) != 3 {
				t.Fatalf("review options = %v", m.WorkflowFeedback.Options)
			}
		}
	}
	if !found {
		t.Fatal("workflow_feedback card missing")
	}

	_, err = fx.wfMgr.Feedback.ResolveUserDecision(fx.ctx, fx.sess.ID, run.ID, phaseID, []string{"Approve"}, "")
	testutil.FailErr(t, "ResolveUserDecision", err)
	vars, err := fx.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if _, still := vars["last_user_ask_response"]; still {
		t.Fatal("last_user_ask_response latch must not be stamped")
	}
	msgs, err = fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages after resolve", err)
	var card *wire.WorkflowFeedbackMeta
	for _, m := range msgs {
		if m.WorkflowFeedback != nil && m.WorkflowFeedback.PhaseID == phaseID {
			card = m.WorkflowFeedback
			break
		}
	}
	if card == nil || card.Answer != "Approve" {
		t.Fatalf("card = %+v want Answer Approve", card)
	}
}

func TestAskUserArtifactRejects(t *testing.T) {
	fx := setupAskUserIntegration(t)
	ctx := context.Background()
	store := visual.NewMemoryStore()
	fx.wfMgr.SetVisualArtifacts(store, func(_ context.Context, sessionID string) string {
		return sessionID
	})
	_, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	_, err = fx.runAskUser(ctx, map[string]any{
		"prompt":    "with missing",
		"artifacts": []any{"missing-id"},
	}, workspaceAskContext(fx, "call_missing"))
	if err == nil || !strings.Contains(err.Error(), "ASK_USER_ARTIFACT_NOT_FOUND") {
		t.Fatalf("want NOT_FOUND, got %v", err)
	}

	foreign, err := store.Put(t.Context(), "other-root", visual.Entry{
		Meta:  wire.VisualArtifact{Mime: "image/png", Source: wire.VisualArtifactSourceRender},
		Bytes: visual.TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "Put foreign", err)
	_, err = fx.runAskUser(ctx, map[string]any{
		"prompt":    "foreign",
		"artifacts": []any{foreign.ID},
	}, tools.ToolContext{SessionID: fx.sess.ID, Agent: orchestration.ProfileCoordinator})
	if err == nil || !strings.Contains(err.Error(), "ASK_USER_ARTIFACT_FOREIGN") {
		t.Fatalf("want FOREIGN, got %v", err)
	}

	unsupported, err := store.Put(t.Context(), fx.sess.ID, visual.Entry{
		Meta:  wire.VisualArtifact{Mime: "application/vnd.lycaon.filmstrip+zip", Source: wire.VisualArtifactSourceRender},
		Bytes: visual.TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "Put unsupported", err)
	_, err = fx.runAskUser(ctx, map[string]any{
		"prompt":    "unsupported",
		"artifacts": []any{unsupported.ID},
	}, tools.ToolContext{SessionID: fx.sess.ID, Agent: orchestration.ProfileCoordinator})
	if err == nil || !strings.Contains(err.Error(), "ASK_USER_ARTIFACT_UNSUPPORTED") {
		t.Fatalf("want UNSUPPORTED, got %v", err)
	}
}

func TestPendingRequestDecisionDoesNotTripHasPendingUserInput(t *testing.T) {
	vars := map[string]any{}
	if scaffoldvars.HasPendingUserInput(vars) {
		t.Fatal("empty vars must not be pending")
	}
}

// workspaceAskContext is a coordinator tool context whose primary root is the
// session workspace, as the executor supplies it.
func workspaceAskContext(fx *askUserFixture, toolCallID string) tools.ToolContext {
	return tools.ToolContext{
		SessionID:    fx.sess.ID,
		ToolCallID:   toolCallID,
		Agent:        orchestration.ProfileCoordinator,
		Roots:        []projectroot.RootRef{{ID: "r1", Path: fx.sess.WorkspacePath, IsPrimary: true}},
		ActiveRootID: "r1",
	}
}

func startWorkspaceAsk(t *testing.T) (*askUserFixture, *visual.MemoryStore) {
	t.Helper()
	fx := setupAskUserIntegration(t)
	store := visual.NewMemoryStore()
	fx.wfMgr.SetVisualArtifacts(store, func(_ context.Context, sessionID string) string {
		return sessionID
	})
	_, err := fx.wfMgr.Starts.StartHuman(context.Background(), fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	return fx, store
}

func writeWorkspaceFile(t *testing.T, fx *askUserFixture, rel string, body []byte) {
	t.Helper()
	abs := filepath.Join(fx.sess.WorkspacePath, rel)
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write "+rel, os.WriteFile(abs, body, 0o644))
}

const workspaceSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><circle cx="50" cy="50" r="40" fill="red"/></svg>`

func TestAskUserWorkspaceFileArtifact(t *testing.T) {
	fx, store := startWorkspaceAsk(t)
	ctx := context.Background()
	writeWorkspaceFile(t, fx, "mockups/test-mockup.svg", []byte(workspaceSVG))

	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt":    "Approve authored SVG mockup?",
		"artifacts": []any{"mockups/test-mockup.svg"},
	}, workspaceAskContext(fx, "call_ws"))
	testutil.FailErr(t, "ask_user with workspace SVG", err)

	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	phaseID, _ := body["phase_id"].(string)
	msgs, err := fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	var found bool
	for _, m := range msgs {
		if m.WorkflowFeedback == nil || m.WorkflowFeedback.PhaseID != phaseID {
			continue
		}
		found = true
		res := store.Resolve(ctx, fx.sess.ID, m.WorkflowFeedback.ArtifactID)
		if !res.IsPresent() {
			t.Fatalf("artifact %q not present in store", m.WorkflowFeedback.ArtifactID)
		}
		meta := res.Meta()
		if meta.Mime != "image/svg+xml" || meta.Source != wire.VisualArtifactSourceWorkspace || meta.Perceive {
			t.Fatalf("stored workspace artifact = %+v", meta)
		}
	}
	if !found {
		t.Fatal("workflow_feedback card missing")
	}
}

func TestAskUserWorkspacePathRejects(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, fx *askUserFixture)
		ref   string
		code  string
	}{
		{name: "missing", ref: "mockups/does-not-exist.svg", code: "ASK_USER_ARTIFACT_NOT_FOUND"},
		{
			name:  "svg that is not xml",
			setup: func(t *testing.T, fx *askUserFixture) { writeWorkspaceFile(t, fx, "bad.svg", []byte("<svg><g></svg>")) },
			ref:   "bad.svg", code: "ASK_USER_ARTIFACT_UNSUPPORTED",
		},
		{
			name:  "png that is not png",
			setup: func(t *testing.T, fx *askUserFixture) { writeWorkspaceFile(t, fx, "fake.png", []byte(workspaceSVG)) },
			ref:   "fake.png", code: "ASK_USER_ARTIFACT_UNSUPPORTED",
		},
		{
			name: "video has no decoder",
			setup: func(t *testing.T, fx *askUserFixture) {
				writeWorkspaceFile(t, fx, "clip.mp4", []byte("\x00\x00\x00\x18ftypmp42"))
			},
			ref: "clip.mp4", code: "ASK_USER_ARTIFACT_UNSUPPORTED",
		},
		{
			name: "symlink out of the root",
			setup: func(t *testing.T, fx *askUserFixture) {
				outside := filepath.Join(t.TempDir(), "outside.svg")
				testutil.FailErr(t, "write outside", os.WriteFile(outside, []byte(workspaceSVG), 0o644))
				testutil.FailErr(t, "symlink", os.Symlink(outside, filepath.Join(fx.sess.WorkspacePath, "link.svg")))
			},
			ref: "link.svg",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx, store := startWorkspaceAsk(t)
			if tc.setup != nil {
				tc.setup(t, fx)
			}
			_, err := fx.runAskUser(context.Background(), map[string]any{
				"prompt": "Review?", "artifacts": []any{tc.ref},
			}, workspaceAskContext(fx, "call_"+tc.name))
			if err == nil {
				t.Fatalf("ask with %q was admitted", tc.ref)
			}
			if tc.code != "" && !strings.Contains(err.Error(), tc.code) {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			if items, _ := store.ListTree(context.Background(), fx.sess.ID); len(items) != 0 {
				t.Fatalf("refused ask stored %d artifacts", len(items))
			}
		})
	}
}

// A dot-prefixed file name is inside the root; containment is a path fact,
// not a string prefix.
func TestAskUserWorkspaceDotDotPrefixedName(t *testing.T) {
	fx, _ := startWorkspaceAsk(t)
	writeWorkspaceFile(t, fx, "..mockup.svg", []byte(workspaceSVG))
	_, err := fx.runAskUser(context.Background(), map[string]any{
		"prompt": "Review?", "artifacts": []any{"..mockup.svg"},
	}, workspaceAskContext(fx, "call_dotdot"))
	testutil.FailErr(t, "ask_user with ..-prefixed name", err)
}

// An ask refused as already pending copies nothing into the store.
func TestAskUserWorkspacePathNotCopiedWhenPending(t *testing.T) {
	fx, store := startWorkspaceAsk(t)
	ctx := context.Background()
	writeWorkspaceFile(t, fx, "a.svg", []byte(workspaceSVG))
	_, err := fx.runAskUser(ctx, map[string]any{"prompt": "First?"}, workspaceAskContext(fx, "call_first"))
	testutil.FailErr(t, "first ask", err)
	_, err = fx.runAskUser(ctx, map[string]any{
		"prompt": "Second?", "artifacts": []any{"a.svg"},
	}, workspaceAskContext(fx, "call_second"))
	if err == nil || !strings.Contains(err.Error(), "ASK_USER_ALREADY_PENDING") {
		t.Fatalf("err = %v, want ASK_USER_ALREADY_PENDING", err)
	}
	if items, _ := store.ListTree(ctx, fx.sess.ID); len(items) != 0 {
		t.Fatalf("pending refusal stored %d artifacts", len(items))
	}
}
