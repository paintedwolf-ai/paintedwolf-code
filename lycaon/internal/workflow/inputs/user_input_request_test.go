package inputs_test

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
	"testing"
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
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	request := workflowinputs.UserInputRequest{
		Prompt:       "Which API?",
		ResponseType: workflowdef.FeedbackResponseText,
		ToolCallID:   "ask-call-1",
	}
	handle, err := mgr.Asks.RequestUserInput(ctx, "sess-1", request)
	testutil.FailErr(t, "RequestUserInput", err)
	if !strings.HasPrefix(handle.PhaseID, "ask-") {
		t.Fatalf("phase_id = %q", handle.PhaseID)
	}
	committed, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "get committed ask", err)
	replayed, err := mgr.Asks.RequestUserInput(ctx, "sess-1", request)
	testutil.FailErr(t, "replay RequestUserInput", err)
	afterReplay, err := mgr.Store.Runs.Get(ctx, run.ID)
	testutil.FailErr(t, "get replayed ask", err)
	if replayed.PhaseID != handle.PhaseID || afterReplay.Revision != committed.Revision {
		t.Fatalf("replay = %+v revision=%d want phase=%s revision=%d", replayed, afterReplay.Revision, handle.PhaseID, committed.Revision)
	}
	request.Prompt = "Which protocol?"
	if _, err := mgr.Asks.RequestUserInput(ctx, "sess-1", request); err == nil {
		t.Fatal("expected ask operation conflict")
	}
	// Card projection preserves the ask revision.
	preVars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars before announce", err)
	ask, ok := runstate.CoordinatorAskPendingFromVars(preVars)
	if !ok || ask.ID != handle.PhaseID || ask.IssuedRevision != committed.Revision {
		t.Fatalf("pending coordinator ask = %+v ok=%v", ask, ok)
	}
	mgr.Asks.AnnouncePendingAsk(ctx, "sess-1")

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if ask, ok := runstate.CoordinatorAskPendingFromVars(vars); !ok || ask.ID != handle.PhaseID || ask.IssuedRevision != committed.Revision {
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
	mgr.Asks.AnnouncePendingAsk(ctx, "sess-1")
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
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	_, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	first, err := mgr.Asks.RequestUserInput(ctx, "sess-1", workflowinputs.UserInputRequest{
		Prompt: "first"})
	testutil.FailErr(t, "first ask", err)
	_, err = mgr.Asks.RequestUserInput(ctx, "sess-1", workflowinputs.UserInputRequest{
		Prompt: "second"})
	reject := &workflowinputs.AskUserReject{}
	ok := errors.As(err, &reject)
	if !ok || reject.Code != "ASK_USER_ALREADY_PENDING" {
		t.Fatalf("err = %v want ASK_USER_ALREADY_PENDING", err)
	}
	if reject.Data["pending_input_id"] != first.PhaseID {
		t.Fatalf("refusal lost existing pending request identity: %+v", reject.Data)
	}
}

func TestCoordinatorAskDoesNotResolveFromGenericChat(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"ask-user-host@1.0.0": askUserTestManifest()})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	handle, err := mgr.Asks.RequestUserInput(ctx, "sess-1", workflowinputs.UserInputRequest{Prompt: "Which API?"})
	testutil.FailErr(t, "RequestUserInput", err)
	testutil.FailErr(t, "TryResolveUserFeedback", mgr.Feedback.TryResolveUserFeedback(ctx, "sess-1", "message-1", testutil.HostOwner().ID, "REST"))
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	ask, ok := runstate.CoordinatorAskPendingFromVars(vars)
	if !ok || ask.ID != handle.PhaseID || ask.Response != "" {
		t.Fatalf("generic chat resolved coordinator ask: %+v ok=%v", ask, ok)
	}
}

func TestRequestUserInputParallelOnePending(t *testing.T) {
	mgr, store, _, _ := testManagerWithRegistry(t)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	_, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	const n = 8
	type result struct {
		handle workflowinputs.UserInputHandle
		err    error
	}
	results := make([]result, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			h, e := mgr.Asks.RequestUserInput(ctx, "sess-1", workflowinputs.UserInputRequest{
				Prompt:       fmt.Sprintf("parallel-%d", i),
				ResponseType: workflowdef.FeedbackResponseSingleChoice,
				Options:      []string{"a", "b"},
			})
			results[i] = result{handle: h, err: e}
			if e == nil {
				mgr.Asks.AnnouncePendingAsk(ctx, "sess-1")
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
		reject := &workflowinputs.AskUserReject{}
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

func TestRequestUserInputChoicePending(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	handle, err := mgr.Asks.RequestUserInput(ctx, "sess-1", workflowinputs.UserInputRequest{
		Prompt:       "Pick one",
		ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Options:      []string{"REST", "GraphQL"},
	})
	testutil.FailErr(t, "RequestUserInput choice", err)
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	ask, ok := runstate.CoordinatorAskPendingFromVars(vars)
	if !ok || ask.ID != handle.PhaseID || ask.ResponseType != workflowdef.FeedbackResponseSingleChoice {
		t.Fatalf("pending choice ask = %+v ok=%v", ask, ok)
	}
}
