//go:build integration

package inputs_test

import (
	"context"
	"encoding/json"
	"errors"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func askUserHostManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "ask-user-int",
		Version: "1.0.0",
		Controls: workflowdef.ManifestControls{
			PhaseAdvance: workflowdef.PhaseAdvanceHost,
		},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "work",
			CompleteWhen: "user_feedback_received:work",
			Next:         "done",
		}, {ID: "done"}},
	})
}

type askUserFixture struct {
	wfMgr   *workflow.RunManager
	sessMgr *session.Host
	sess    *wire.Session
	toolReg *tools.DefaultRegistry
	pending bool
	kicks   []string
	ctx     context.Context
}

func setupAskUserIntegration(t *testing.T) *askUserFixture {
	t.Helper()
	anchorReg, err := anchor.LoadRegistryFromConfigRoot()
	testutil.FailErr(t, "LoadRegistryFromConfigRoot", err)
	anchor.SetDefaultRegistry(anchorReg)
	t.Cleanup(func() { anchor.SetDefaultRegistry(nil) })

	sqlDB := testdbfixture.Open(t, "ask-user-int.db")

	store := store.NewSQL(sqlDB)
	sessMgr := session.NewHost(store, session.Models{Client: llm.NewMockProvider(nil), Limits: settings.DefaultSessionLimits()}, tools.NewStubRegistry())
	testutil.FailErr(t, "install anchor registry", sessMgr.Coordinator.Guidance.InstallAnchorRegistry())

	manifest := askUserHostManifest()
	manifestReg := workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		manifest.ID + "@" + manifest.Version: manifest,
	})
	wfMgr := workflow.NewManager(workflowpersistence.New(sqlDB), store, manifestReg, nil)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	testutil.FailErr(t, "conditions registry", err)
	wfMgr.SetConditionRegistry(reg)

	fx := &askUserFixture{
		wfMgr:   wfMgr,
		sessMgr: sessMgr,
		ctx:     testdbseed.OwnerCaller(t, context.Background(), sqlDB),
	}
	wfMgr.Feedback.OnFeedbackPending = func(_ context.Context, sessionID, _ string) {
		fx.pending = true
		fx.kicks = append(fx.kicks, anchor.InformRender(anchor.FeedbackPending))
		sessMgr.Coordinator.Guidance.Emit(context.Background(), sessionID, anchor.FeedbackPending, anchor.Envelope{})
	}
	wfMgr.Feedback.OnFeedbackResolved = func(_ context.Context, sessionID, _, _, _ string) {
		fx.kicks = append(fx.kicks, anchor.InformRender(anchor.FeedbackReceived))
		sessMgr.Coordinator.Guidance.Drop(sessionID, anchor.FeedbackPending)
		sessMgr.Coordinator.Guidance.Emit(context.Background(), sessionID, anchor.FeedbackReceived, anchor.Envelope{})
	}

	toolReg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "workflowinputs.RegisterAskUserTool", workflowinputs.RegisterAskUserTool(toolReg, wfMgr.Asks, nil))
	fx.toolReg = toolReg

	dir := t.TempDir()
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, dir)
	sess, err := store.Create(context.Background(), wire.CreateSessionRequest{
		Posture: wire.SessionPostureSpec,
	}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	fx.sess = sess
	sessMgr.SetWorkflowDomains(&session.WorkflowDomains{Runs: wfMgr.Store.Runs, Policy: wfMgr.Policy, Ambient: wfMgr.Ambient, Blueprints: wfMgr.Blueprints, Batch: wfMgr.Batch, Slash: wfMgr.Slash, Requests: wfMgr.Requests, Feedback: wfMgr.Feedback, Transcript: wfMgr.Transcript, Asks: wfMgr.Asks, Fanout: wfMgr.Fanout, Phases: wfMgr.Phases, Reports: wfMgr.Reports, Recovery: wfMgr.Recovery, Cleanup: wfMgr})
	return fx
}

// runAskUser opens and announces the question; callers append tool rows separately.
func (fx *askUserFixture) runAskUser(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
	out, err := fx.toolReg.Run(ctx, "ask_user", args, tctx)
	if err == nil {
		fx.wfMgr.Asks.AnnouncePendingAsk(ctx, tctx.Identity.SessionID)
	}
	return out, err
}

func TestAskUserTextResolveAndKick(t *testing.T) {
	fx := setupAskUserIntegration(t)
	ctx := fx.ctx
	run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt": "REST or GraphQL?",
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: fx.sess.ID,
		Agent:      orchestration.ProfileCoordinator,
		ToolCallID: "call_text"}})
	testutil.FailErr(t, "ask_user", err)
	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	phaseID, _ := body["phase_id"].(string)
	if !strings.HasPrefix(phaseID, "ask-") {
		t.Fatalf("phase_id = %q body=%s", phaseID, out)
	}
	if !fx.pending {
		t.Fatal("expected OnFeedbackPending")
	}
	testutil.FailErr(t, "AppendMessages", fx.wfMgr.Policy.Sessions.(session.Store).AppendMessages(ctx, fx.sess.ID, wire.Message{
		ID:      "msg-ask-text",
		Role:    wire.MessageRoleTool,
		Content: out,
		ToolResult: &wire.ToolResult{
			Tool:       "ask_user",
			ToolCallID: "call_text",
			Content:    out,
		},
	}))

	_, err = fx.wfMgr.Feedback.ResolveUserFeedback(ctx, fx.sess.ID, run.ID, phaseID, "GraphQL please")
	testutil.FailErr(t, "ResolveUserFeedback", err)

	vars, err := fx.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if scaffoldvars.HasPendingUserInput(vars) {
		t.Fatal("pending should clear")
	}
	if _, still := vars["last_user_ask_response"]; still {
		t.Fatal("last_user_ask_response latch must not be stamped")
	}
	msgs, err := fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages after", err)
	var answered bool
	for _, m := range msgs {
		if m.Role != wire.MessageRoleTool || m.ToolResult == nil || m.ToolResult.ToolCallID != "call_text" {
			continue
		}
		var got map[string]any
		testutil.FailErr(t, "unmarshal answer", json.Unmarshal([]byte(m.Content), &got))
		if got["status"] != "answered" || got["response"] != "GraphQL please" || got["resolved_by"] != "user" {
			t.Fatalf("answered body = %v", got)
		}
		answered = true
	}
	if !answered {
		t.Fatal("ask_user tool row not rewritten")
	}
	gotPending, gotReceived := false, false
	for _, id := range fx.kicks {
		switch id {
		case anchor.InformRender(anchor.FeedbackPending):
			gotPending = true
		case anchor.InformRender(anchor.FeedbackReceived):
			gotReceived = true
		}
	}
	if !gotPending || !gotReceived {
		t.Fatalf("kicks = %#v", fx.kicks)
	}

	// Answer delivery replaces the pending-feedback kick.
	if ids := fx.sessMgr.Coordinator.Guidance.PendingIDs(ctx, fx.sess.ID); !slices.Contains(ids, anchor.InformRender(anchor.FeedbackReceived)) {
		t.Fatalf("pending kicks after resolve = %v want %s", ids, anchor.InformRender(anchor.FeedbackReceived))
	}
}

func TestAskUserSecretNeverPersistsTheRawResponse(t *testing.T) {
	fx := setupAskUserIntegration(t)
	ctx := fx.ctx
	run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	const raw = "super-secret-token"
	const reference = "{{paintedwolf-secret:aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}}"
	var captured workflowinputs.SecretCaptureRequest
	fx.wfMgr.Asks.SetSecretCapture(func(_ context.Context, req workflowinputs.SecretCaptureRequest) (workflowinputs.SecretCaptureResult, error) {
		captured = req
		return workflowinputs.SecretCaptureResult{Reference: reference}, nil
	})
	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt": "Provide the registry token", "response_type": "secret",
		"secret": map[string]any{"name": "Registry token", "purpose": "Authenticate publishing", "scope": "chat"},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: fx.sess.ID,
		Agent:      orchestration.ProfileCoordinator,
		ToolCallID: "call_secret"}})
	testutil.FailErr(t, "ask_user secret", err)
	var pending map[string]any
	testutil.FailErr(t, "decode pending", json.Unmarshal([]byte(out), &pending))
	phaseID, _ := pending["phase_id"].(string)
	testutil.FailErr(t, "append ask tool row", fx.wfMgr.Policy.Sessions.(session.Store).AppendMessages(ctx, fx.sess.ID, wire.Message{
		ID: "msg-ask-secret", Role: wire.MessageRoleTool, Content: out,
		ToolResult: &wire.ToolResult{Tool: "ask_user", ToolCallID: "call_secret", Content: out},
	}))

	_, err = fx.wfMgr.Asks.ResolveUserSecret(ctx, fx.sess.ID, run.ID, phaseID, raw)
	testutil.FailErr(t, "ResolveUserSecret", err)
	if captured.Value != raw || captured.Name != "Registry token" || captured.Scope != "chat" {
		t.Fatalf("capture = %+v", captured)
	}
	vars, err := fx.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	encodedVars, err := json.Marshal(vars)
	testutil.FailErr(t, "encode vars", err)
	if strings.Contains(string(encodedVars), raw) || !strings.Contains(string(encodedVars), reference) {
		t.Fatalf("workflow vars leaked or lost the reference: %s", encodedVars)
	}
	msgs, err := fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	encodedMessages, err := json.Marshal(msgs)
	testutil.FailErr(t, "encode messages", err)
	if strings.Contains(string(encodedMessages), raw) || !strings.Contains(string(encodedMessages), reference) {
		t.Fatalf("transcript leaked or lost the reference: %s", encodedMessages)
	}
}

func TestAskUserSecretDiscardsNewCapabilityWhenThePendingAskTurnsStale(t *testing.T) {
	fx := setupAskUserIntegration(t)
	ctx := fx.ctx
	run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)
	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt": "Provide the registry token", "response_type": "secret",
		"secret": map[string]any{"name": "Registry token", "purpose": "Authenticate publishing", "scope": "chat"},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: fx.sess.ID,
		Agent:      orchestration.ProfileCoordinator,
		ToolCallID: "call_secret_stale"}})
	testutil.FailErr(t, "ask_user secret", err)
	var pending map[string]any
	testutil.FailErr(t, "decode pending", json.Unmarshal([]byte(out), &pending))
	phaseID, _ := pending["phase_id"].(string)

	const reference = "{{paintedwolf-secret:bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb}}"
	var discarded int
	fx.wfMgr.Asks.SetSecretCapture(func(_ context.Context, _ workflowinputs.SecretCaptureRequest) (workflowinputs.SecretCaptureResult, error) {
		_, pauseErr := fx.wfMgr.Controls.Pause(context.Background(), run.ID, "concurrent human action")
		if pauseErr != nil {
			return workflowinputs.SecretCaptureResult{}, pauseErr
		}
		return workflowinputs.SecretCaptureResult{
			Reference: reference,
			Discard: func(context.Context) error {
				discarded++
				return nil
			},
		}, nil
	})
	_, err = fx.wfMgr.Asks.ResolveUserSecret(ctx, fx.sess.ID, run.ID, phaseID, "x")
	if !errors.Is(err, runstate.ErrRevisionConflict) {
		t.Fatalf("stale secret resolution error = %v", err)
	}
	if discarded != 1 {
		t.Fatalf("discard calls = %d, want 1", discarded)
	}
	vars, varsErr := fx.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", varsErr)
	encoded, encodeErr := json.Marshal(vars)
	testutil.FailErr(t, "encode vars", encodeErr)
	if strings.Contains(string(encoded), reference) || strings.Contains(string(encoded), `"x"`) {
		t.Fatalf("failed resolution persisted protected input: %s", encoded)
	}
}

func TestAskUserSingleChoiceSyntheticResolve(t *testing.T) {
	fx := setupAskUserIntegration(t)
	ctx := fx.ctx
	run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt":        "Pick API",
		"response_type": "single_choice",
		"options":       []any{"REST", "GraphQL"},
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: fx.sess.ID,
		Agent: orchestration.ProfileCoordinator}})
	testutil.FailErr(t, "ask_user", err)
	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	phaseID, _ := body["phase_id"].(string)

	_, err = fx.wfMgr.Feedback.ResolveUserDecision(ctx, fx.sess.ID, run.ID, phaseID, []string{"GraphQL"}, "")
	testutil.FailErr(t, "ResolveUserDecision", err)
	vars, err := fx.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if scaffoldvars.HasPendingUserInput(vars) {
		t.Fatal("pending should clear")
	}
}

func TestAskUserCardOrdFollowsToolRow(t *testing.T) {
	fx := setupAskUserIntegration(t)
	ctx := fx.ctx
	run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
	})
	testutil.FailErr(t, "StartHuman", err)

	out, err := fx.toolReg.Run(ctx, "ask_user", map[string]any{
		"prompt": "REST or GraphQL?",
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: fx.sess.ID,
		Agent:      orchestration.ProfileCoordinator,
		ToolCallID: "call_ord"}})
	testutil.FailErr(t, "ask_user", err)
	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	phaseID, _ := body["phase_id"].(string)

	// The prompt loop commits the tool row after the tool returns, then announces.
	testutil.FailErr(t, "AppendMessages", fx.wfMgr.Policy.Sessions.(session.Store).AppendMessages(ctx, fx.sess.ID, wire.Message{
		ID:      "msg-ask-ord",
		Role:    wire.MessageRoleTool,
		Content: out,
		ToolResult: &wire.ToolResult{
			Tool:       "ask_user",
			ToolCallID: "call_ord",
			Content:    out,
		},
	}))
	fx.wfMgr.Asks.AnnouncePendingAsk(ctx, fx.sess.ID)

	toolOrd, cardOrd := askUserOrdPair(t, fx, ctx, phaseID, "call_ord")
	if cardOrd <= toolOrd {
		t.Fatalf("card ord = %d, tool row ord = %d — card must sort after its chip", cardOrd, toolOrd)
	}

	_, err = fx.wfMgr.Feedback.ResolveUserFeedback(ctx, fx.sess.ID, run.ID, phaseID, "GraphQL please")
	testutil.FailErr(t, "ResolveUserFeedback", err)

	afterToolOrd, afterCardOrd := askUserOrdPair(t, fx, ctx, phaseID, "call_ord")
	if afterToolOrd != toolOrd || afterCardOrd != cardOrd {
		t.Fatalf("ord moved on resolve: tool %d→%d card %d→%d", toolOrd, afterToolOrd, cardOrd, afterCardOrd)
	}
}

// askUserOrdPair reads the tool and card positions for one question.
func askUserOrdPair(t *testing.T, fx *askUserFixture, ctx context.Context, phaseID, toolCallID string) (toolOrd, cardOrd int64) {
	t.Helper()
	msgs, err := fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	for _, m := range msgs {
		if m.ToolResult != nil && m.ToolResult.ToolCallID == toolCallID {
			toolOrd = m.Ord
		}
		if m.WorkflowFeedback != nil && m.WorkflowFeedback.PhaseID == phaseID {
			cardOrd = m.Ord
		}
	}
	if toolOrd == 0 {
		t.Fatalf("ask_user tool row %q missing or unstamped ord", toolCallID)
	}
	if cardOrd == 0 {
		t.Fatalf("workflow_feedback card for %q missing or unstamped ord", phaseID)
	}
	return toolOrd, cardOrd
}

func TestAskUserCoordinatorOnly(t *testing.T) {
	fx := setupAskUserIntegration(t)
	_, err := fx.toolReg.Run(context.Background(), "ask_user", map[string]any{
		"prompt": "x",
	}, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: fx.sess.ID,
		Agent: "implementer"}})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "coordinator") {
		t.Fatalf("err = %v", err)
	}
}
