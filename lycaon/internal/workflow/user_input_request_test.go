package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func askUserTestManifest() workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "ask-user-host",
		Version: "1.0.0",
		Controls: workflowdef.ManifestControls{
			PhaseAdvance: workflowdef.PhaseAdvanceHost,
		},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:            "work",
			ActivityLabel: "Waiting for input",
			CompleteWhen:  "user_feedback_received:work",
			Next:          "done",
		}, {ID: "done", ActivityLabel: "Done"}},
	})
}

func TestRequestUserInputPendingVarsAndAnnounce(t *testing.T) {
	mgr, store, _, _ := testManagerWithRegistry(t)
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	request := UserInputRequest{
		Prompt:       "Which API?",
		ResponseType: workflowdef.FeedbackResponseText,
		ToolCallID:   "ask-call-1",
	}
	handle, err := mgr.RequestUserInput(ctx, "sess-1", request)
	testutil.FailErr(t, "RequestUserInput", err)
	if !strings.HasPrefix(handle.PhaseID, "ask-") {
		t.Fatalf("phase_id = %q", handle.PhaseID)
	}
	committed, err := mgr.Store.Get(ctx, run.ID)
	testutil.FailErr(t, "get committed ask", err)
	replayed, err := mgr.RequestUserInput(ctx, "sess-1", request)
	testutil.FailErr(t, "replay RequestUserInput", err)
	afterReplay, err := mgr.Store.Get(ctx, run.ID)
	testutil.FailErr(t, "get replayed ask", err)
	if replayed.PhaseID != handle.PhaseID || afterReplay.Revision != committed.Revision {
		t.Fatalf("replay = %+v revision=%d want phase=%s revision=%d", replayed, afterReplay.Revision, handle.PhaseID, committed.Revision)
	}
	request.Prompt = "Which protocol?"
	if _, err := mgr.RequestUserInput(ctx, "sess-1", request); err == nil {
		t.Fatal("expected ask operation conflict")
	}
	// Card projection preserves the ask revision.
	preVars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars before announce", err)
	ask, ok := coordinatorAskPendingFromVars(preVars)
	if !ok || ask.ID != handle.PhaseID || ask.IssuedRevision != committed.Revision {
		t.Fatalf("pending coordinator ask = %+v ok=%v", ask, ok)
	}
	mgr.AnnouncePendingAsk(ctx, "sess-1")

	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if ask, ok := coordinatorAskPendingFromVars(vars); !ok || ask.ID != handle.PhaseID || ask.IssuedRevision != committed.Revision {
		t.Fatalf("announced ask = %+v ok=%v", ask, ok)
	}

	msgs, err := store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "GetMessages", err)
	var cards int
	for _, m := range msgs {
		if m.Kind == api.MessageKindWorkflowFeedback && m.WorkflowFeedback != nil && m.WorkflowFeedback.PhaseID == handle.PhaseID {
			cards++
		}
	}
	if cards != 1 {
		t.Fatalf("transcript cards = %d want 1", cards)
	}

	// Re-announcement preserves the message id.
	mgr.AnnouncePendingAsk(ctx, "sess-1")
	msgs, err = store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "GetMessages after reannounce", err)
	cards = 0
	for _, m := range msgs {
		if m.Kind == api.MessageKindWorkflowFeedback && m.WorkflowFeedback != nil && m.WorkflowFeedback.PhaseID == handle.PhaseID {
			cards++
		}
	}
	if cards != 1 {
		t.Fatalf("after reannounce cards = %d want 1", cards)
	}
}

func TestRequestUserInputOnePendingGuard(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	_, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	first, err := mgr.RequestUserInput(ctx, "sess-1", UserInputRequest{
		Prompt: "first"})
	testutil.FailErr(t, "first ask", err)
	_, err = mgr.RequestUserInput(ctx, "sess-1", UserInputRequest{
		Prompt: "second"})
	reject := &AskUserReject{}
	ok := errors.As(err, &reject)
	if !ok || reject.Code != "ASK_USER_ALREADY_PENDING" {
		t.Fatalf("err = %v want ASK_USER_ALREADY_PENDING", err)
	}
	if reject.Data["pending_input_id"] != first.PhaseID {
		t.Fatalf("refusal lost existing pending request identity: %+v", reject.Data)
	}
}

func TestNormalizeSecretAskRejectsUnusableLifecycleMetadataBeforePrompting(t *testing.T) {
	for name, secret := range map[string]*workflowdef.SecretInputSpec{
		"project without purpose": {Name: "Deploy key", Scope: "project"},
		"lifetime beyond a year":  {Name: "Deploy key", Scope: "task", AgentUseTTLSeconds: maxAskSecretAgentUseLifetimeSeconds + 1},
		"name too long":           {Name: strings.Repeat("x", 81), Scope: "task"},
		"purpose too long":        {Name: "Deploy key", Scope: "task", Purpose: strings.Repeat("x", 241)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeAskUserRequest(UserInputRequest{
				Prompt: "Provide it", ResponseType: workflowdef.FeedbackResponseSecret, Secret: secret,
			})
			reject := &AskUserReject{}
			if !errors.As(err, &reject) ||
				(reject.Code != "ASK_USER_SECRET_METADATA_REQUIRED" && reject.Code != "ASK_USER_SECRET_METADATA_INVALID") {
				t.Fatalf("error = %v", err)
			}
			if len(reject.Data) != 1 || reject.Data["field"] == "" {
				t.Fatalf("metadata refusal must identify only the invalid field: %+v", reject.Data)
			}
			for key := range reject.Data {
				if key != "field" {
					t.Fatalf("protected metadata entered feedback at %s", key)
				}
			}
		})
	}
}

func TestCoordinatorAskDoesNotResolveFromGenericChat(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"ask-user-host@1.0.0": askUserTestManifest()})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	handle, err := mgr.RequestUserInput(ctx, "sess-1", UserInputRequest{Prompt: "Which API?"})
	testutil.FailErr(t, "RequestUserInput", err)
	testutil.FailErr(t, "TryResolveUserFeedback", mgr.TryResolveUserFeedback(ctx, "sess-1", "message-1", testutil.HostOwner().ID, "REST"))
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	ask, ok := coordinatorAskPendingFromVars(vars)
	if !ok || ask.ID != handle.PhaseID || ask.Response != "" {
		t.Fatalf("generic chat resolved coordinator ask: %+v ok=%v", ask, ok)
	}
}

func TestRequestUserInputParallelOnePending(t *testing.T) {
	mgr, store, _, _ := testManagerWithRegistry(t)
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	_, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	const n = 8
	type result struct {
		handle UserInputHandle
		err    error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			h, e := mgr.RequestUserInput(ctx, "sess-1", UserInputRequest{
				Prompt:       fmt.Sprintf("parallel-%d", i),
				ResponseType: workflowdef.FeedbackResponseSingleChoice,
				Options:      []string{"a", "b"},
			})
			results[i] = result{handle: h, err: e}
			if e == nil {
				mgr.AnnouncePendingAsk(ctx, "sess-1")
			}
		}(i)
	}
	wg.Wait()

	var okCount, alreadyPending int
	for _, r := range results {
		if r.err == nil {
			okCount++
			continue
		}
		reject := &AskUserReject{}
		isReject := errors.As(r.err, &reject)
		if isReject && reject.Code == "ASK_USER_ALREADY_PENDING" {
			alreadyPending++
			continue
		}
		t.Fatalf("unexpected err: %v", r.err)
	}
	if okCount != 1 {
		t.Fatalf("successful opens = %d want 1", okCount)
	}
	if alreadyPending != n-1 {
		t.Fatalf("ASK_USER_ALREADY_PENDING = %d want %d", alreadyPending, n-1)
	}

	msgs, err := store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "GetMessages", err)
	var cards int
	for _, m := range msgs {
		if m.Kind == api.MessageKindWorkflowFeedback {
			cards++
		}
	}
	if cards != 1 {
		t.Fatalf("transcript cards = %d want 1 (no ghost cards)", cards)
	}
}

func TestPendingFeedbackFromVarsIncludesChoice(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	handle, err := mgr.RequestUserInput(ctx, "sess-1", UserInputRequest{
		Prompt:       "Pick a color",
		ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Options:      []string{"red", "green"},
	})
	testutil.FailErr(t, "RequestUserInput", err)

	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	pf, ok := PendingFeedbackFromVars(vars)
	if !ok || pf.PhaseID != handle.PhaseID || pf.Prompt != "Pick a color" {
		t.Fatalf("PendingFeedbackFromVars = %+v ok=%v", pf, ok)
	}
	// Response type selects the answer endpoint; options define the available choices.
	if pf.ResponseType != string(workflowdef.FeedbackResponseSingleChoice) {
		t.Fatalf("response_type = %q want single_choice", pf.ResponseType)
	}
	if len(pf.Options) != 2 || pf.Options[0] != "red" || pf.Options[1] != "green" {
		t.Fatalf("options = %v want [red green]", pf.Options)
	}
	ui, err := mgr.ComputeRunUI(ctx, run)
	testutil.FailErr(t, "ComputeRunUI", err)
	if ui == nil || ui.PendingFeedback == nil || ui.PendingFeedback.PhaseID != handle.PhaseID {
		t.Fatalf("run UI pending_feedback = %+v", ui)
	}
	if ui.PendingFeedback.ResponseType != string(workflowdef.FeedbackResponseSingleChoice) || len(ui.PendingFeedback.Options) != 2 {
		t.Fatalf("run UI pending_feedback missing choice meta: %+v", ui.PendingFeedback)
	}
}

func TestPendingFeedbackFromVarsTextResponseType(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	handle, err := mgr.RequestUserInput(ctx, "sess-1", UserInputRequest{
		Prompt:       "Describe the goal",
		ResponseType: workflowdef.FeedbackResponseText,
	})
	testutil.FailErr(t, "RequestUserInput", err)

	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	pf, ok := PendingFeedbackFromVars(vars)
	if !ok || pf.PhaseID != handle.PhaseID {
		t.Fatalf("PendingFeedbackFromVars = %+v ok=%v", pf, ok)
	}
	if pf.ResponseType != string(workflowdef.FeedbackResponseText) || len(pf.Options) != 0 {
		t.Fatalf("text ask projection = %+v want response_type=text, no options", pf)
	}
}

func TestRequestUserInputChoicePending(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	handle, err := mgr.RequestUserInput(ctx, "sess-1", UserInputRequest{
		Prompt:       "Pick one",
		ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Options:      []string{"REST", "GraphQL"},
	})
	testutil.FailErr(t, "RequestUserInput choice", err)
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	ask, ok := coordinatorAskPendingFromVars(vars)
	if !ok || ask.ID != handle.PhaseID || ask.ResponseType != workflowdef.FeedbackResponseSingleChoice {
		t.Fatalf("pending choice ask = %+v ok=%v", ask, ok)
	}
}

func TestAskInputNeverDowngradesProtectedChannel(t *testing.T) {
	for _, refs := range [][]string{nil, {"one"}, {"one", "two"}} {
		for _, responseType := range []workflowdef.FeedbackResponseType{workflowdef.FeedbackResponseSecret, workflowdef.FeedbackResponseText, "invalid"} {
			req := UserInputRequest{Prompt: "Provide input", ResponseType: responseType, Artifacts: refs, Secret: &workflowdef.SecretInputSpec{Name: "Key"}}
			norm, err := normalizeAskUserRequest(req)
			if responseType == workflowdef.FeedbackResponseSecret && len(refs) == 0 {
				testutil.FailErr(t, "normalize protected request", err)
				if norm.rt != workflowdef.FeedbackResponseSecret || norm.secret == nil || norm.purpose != "secret" {
					t.Fatalf("protected request downgraded: %+v", norm)
				}
				continue
			}
			var reject *AskUserReject
			if !errors.As(err, &reject) {
				t.Fatalf("incompatible request created a card: %+v", norm)
			}
			want := "ASK_USER_SECRET_METADATA_FORBIDDEN"
			if responseType == workflowdef.FeedbackResponseSecret {
				want = "ASK_USER_SECRET_ARTIFACTS_FORBIDDEN"
			}
			if responseType == "invalid" {
				want = "ASK_USER_RESPONSE_TYPE_INVALID"
			}
			if string(reject.Code) != want {
				t.Fatalf("type=%s artifacts=%d code=%s want=%s", responseType, len(refs), reject.Code, want)
			}
		}
	}
}
