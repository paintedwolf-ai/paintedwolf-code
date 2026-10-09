//go:build integration

package inputs_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	session "github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestAskUserComparePreference(t *testing.T) {
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

	put := func(caption, handle string) string {
		art, err := store.Put(t.Context(), fx.sess.ID, visual.Entry{
			Meta: wire.VisualArtifact{
				Mime: "image/png", Source: wire.VisualArtifactSourceCapture,
				Caption: caption, EvidenceHandle: handle,
			},
			Bytes: visual.TestPNG1x1Bytes(),
		})
		testutil.FailErr(t, "Put "+caption, err)
		return art.ID
	}
	idA, idB := put("A", "render#1"), put("B", "render#2")

	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt":    "Which layout?",
		"purpose":   "compare",
		"artifacts": []any{idA, idB},
	}, tools.ToolContext{SessionID: fx.sess.ID, Agent: orchestration.ProfileCoordinator, ToolCallID: "call_compare"})
	testutil.FailErr(t, "ask_user", err)
	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	phaseID, _ := body["phase_id"].(string)
	testutil.FailErr(t, "AppendMessages", fx.wfMgr.Policy.Sessions.(session.Store).AppendMessages(ctx, fx.sess.ID, wire.Message{
		ID:      "msg-ask-compare",
		Role:    wire.MessageRoleTool,
		Content: out,
		ToolResult: &wire.ToolResult{
			Tool:       "ask_user",
			ToolCallID: "call_compare",
			Content:    out,
		},
	}))

	msgs, err := fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	var found bool
	for _, m := range msgs {
		if m.WorkflowFeedback != nil && m.WorkflowFeedback.PhaseID == phaseID {
			found = true
			if m.WorkflowFeedback.Purpose != "compare" {
				t.Fatalf("purpose = %q", m.WorkflowFeedback.Purpose)
			}
			if len(m.WorkflowFeedback.ArtifactIDs) != 2 {
				t.Fatalf("artifact_ids = %v", m.WorkflowFeedback.ArtifactIDs)
			}
			if len(m.WorkflowFeedback.Options) != 2 ||
				m.WorkflowFeedback.Options[0] != "A" ||
				m.WorkflowFeedback.Options[1] != "B" {
				t.Fatalf("options = %v", m.WorkflowFeedback.Options)
			}
			if !m.WorkflowFeedback.AllowOther {
				t.Fatal("AllowOther must be true")
			}
		}
	}
	if !found {
		t.Fatal("workflow_feedback card missing")
	}

	_, err = fx.wfMgr.Feedback.ResolveUserDecision(fx.ctx, fx.sess.ID, run.ID, phaseID, []string{"B"}, "tighter spacing")
	testutil.FailErr(t, "ResolveUserDecision", err)
	vars, err := fx.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if _, still := vars["last_user_ask_response"]; still {
		t.Fatal("last_user_ask_response latch must not be stamped")
	}

	msgs, err = fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages after", err)
	var answered bool
	for _, m := range msgs {
		if m.Role != wire.MessageRoleTool || m.ToolResult == nil {
			continue
		}
		if m.ToolResult.ToolCallID != "call_compare" {
			continue
		}
		var got map[string]any
		testutil.FailErr(t, "unmarshal answer", json.Unmarshal([]byte(m.Content), &got))
		if got["status"] != "answered" || got["response"] != "B" {
			t.Fatalf("answered body = %v", got)
		}
		if got["purpose"] != "compare" {
			t.Fatalf("purpose = %v want compare", got["purpose"])
		}
		choices, _ := got["choices"].([]any)
		if len(choices) != 1 || choices[0] != "B" {
			t.Fatalf("choices = %v want [B]", got["choices"])
		}
		answered = true
	}
	if !answered {
		t.Fatal("ask_user tool row not rewritten")
	}
}

func TestAskUserCompareOtherEscape(t *testing.T) {
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
	put := func(handle string) string {
		art, err := store.Put(t.Context(), fx.sess.ID, visual.Entry{
			Meta:  wire.VisualArtifact{Mime: "image/png", Source: wire.VisualArtifactSourceCapture, EvidenceHandle: handle},
			Bytes: visual.TestPNG1x1Bytes(),
		})
		testutil.FailErr(t, "Put", err)
		return art.ID
	}
	idA, idB := put("render#1"), put("render#2")
	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt":    "Which?",
		"purpose":   "compare",
		"artifacts": []any{idA, idB},
	}, tools.ToolContext{SessionID: fx.sess.ID, Agent: orchestration.ProfileCoordinator})
	testutil.FailErr(t, "ask_user", err)
	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	phaseID, _ := body["phase_id"].(string)
	_, err = fx.wfMgr.Feedback.ResolveUserDecision(fx.ctx, fx.sess.ID, run.ID, phaseID, []string{"neither works"}, "try a third")
	testutil.FailErr(t, "Resolve Other", err)
	vars, err := fx.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if _, still := vars["last_user_ask_response"]; still {
		t.Fatal("last_user_ask_response latch must not be stamped")
	}
	msgs, err := fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	var card *wire.WorkflowFeedbackMeta
	for _, m := range msgs {
		if m.WorkflowFeedback != nil && m.WorkflowFeedback.PhaseID == phaseID {
			card = m.WorkflowFeedback
			break
		}
	}
	if card == nil || !strings.Contains(card.Answer, "neither works") {
		t.Fatalf("card answer = %+v", card)
	}
}

// Evidence handles resolve to the artifact that carries them in the tree.
func TestAskUserCompareResolvesEvidenceHandles(t *testing.T) {
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
	ids := map[string]string{}
	for _, h := range []string{"render#1", "render#2"} {
		art, err := store.Put(t.Context(), fx.sess.ID, visual.Entry{
			Meta:  wire.VisualArtifact{Mime: "image/png", Source: wire.VisualArtifactSourceRender, EvidenceHandle: h},
			Bytes: visual.TestPNG1x1Bytes(),
		})
		testutil.FailErr(t, "Put "+h, err)
		ids[h] = art.ID
	}
	_, err = fx.runAskUser(ctx, map[string]any{
		"prompt":    "Which?",
		"artifacts": []any{"render#9"},
	}, tools.ToolContext{SessionID: fx.sess.ID, Agent: orchestration.ProfileCoordinator, ToolCallID: "call_unknown"})
	if err == nil || !strings.Contains(err.Error(), "ASK_USER_ARTIFACT_NOT_FOUND") {
		t.Fatalf("unknown handle error = %v", err)
	}
	_, err = fx.runAskUser(ctx, map[string]any{
		"prompt":    "Which?",
		"purpose":   "compare",
		"artifacts": []any{"render#1", "render#2"},
	}, tools.ToolContext{SessionID: fx.sess.ID, Agent: orchestration.ProfileCoordinator, ToolCallID: "call_handles"})
	testutil.FailErr(t, "ask_user by handle", err)
	msgs, err := fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	for _, m := range msgs {
		if fb := m.WorkflowFeedback; fb != nil && len(fb.ArtifactIDs) == 2 {
			if fb.ArtifactIDs[0] != ids["render#1"] || fb.ArtifactIDs[1] != ids["render#2"] {
				t.Fatalf("artifact ids = %v, want %v", fb.ArtifactIDs, ids)
			}
			return
		}
	}
	t.Fatal("compare card with both artifacts missing")
}
