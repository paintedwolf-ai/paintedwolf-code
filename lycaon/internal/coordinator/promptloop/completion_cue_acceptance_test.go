package promptloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/completioncue"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/progress/progresstest"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/store/storetest"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/reporting"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"github.com/lycaon/lycaon/pkg/api"
)

type scriptedStep struct {
	content   string
	toolCalls []api.ToolCall
	err       error
}

type capturingScriptLLM struct {
	steps    []scriptedStep
	requests []modelcall.CompletionRequest
	idx      int
}

func (c *capturingScriptLLM) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.requests = append(c.requests, req)
	if c.idx >= len(c.steps) {
		return &modelcall.Completion{Content: "done"}, nil
	}
	step := c.steps[c.idx]
	c.idx++
	if step.err != nil {
		return nil, step.err
	}
	return &modelcall.Completion{
		Content:   step.content,
		ToolCalls: step.toolCalls,
	}, nil
}

func (c *capturingScriptLLM) Stream(ctx context.Context, req modelcall.CompletionRequest, _ func(modelcall.StreamEvent)) (*modelcall.Completion, error) {
	return c.Complete(ctx, req)
}

type completionCueHarness struct {
	t            *testing.T
	st           *storetest.Store
	progress     progress.Store
	tracker      *completioncue.Tracker
	hints        *guidance.HintConfig
	sess         *api.Session
	llm          *capturingScriptLLM
	loop         *promptloop.PromptLoop
	reg          *tools.DefaultRegistry
	checkCurrent bool
	oarSawCue    int
	fitSawCue    int
}

func newCompletionCueHarness(t *testing.T, withFeature bool, steps []scriptedStep) *completionCueHarness {
	t.Helper()
	st := storetest.New(t, testdbseed.DefaultProjectID)
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load stock hint config", err)

	prog := progresstest.NewStore(t)
	h := &completionCueHarness{
		t:            t,
		st:           st,
		progress:     prog,
		hints:        hints,
		llm:          &capturingScriptLLM{steps: steps},
		checkCurrent: true,
	}
	if withFeature {
		h.tracker = completioncue.NewTracker(completionCueTestStore{st: st})
	}

	ctx := t.Context()
	sess, err := st.Sessions.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	h.sess = sess

	reg := toolstest.NewRegistry()
	scopeKey := func(_ context.Context, id string) string { return id }
	_ = reg.Register("update_progress", reporting.ProgressHandler(prog, scopeKey, nil))
	_ = reg.Register("read", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return "file content", nil
	})
	_ = reg.Register("verify", func(_ context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if launchOnly, _ := args["launch_only"].(bool); launchOnly {
			return "launched", nil
		}
		verdict := api.SourceVerdictPassed
		if fail, _ := args["fail"].(bool); fail {
			verdict = api.SourceVerdictFailed
		}
		isCheck := true
		if raw, ok := args["is_check"].(bool); ok {
			isCheck = raw
		}
		intentID := tctx.Turn.UserIntentID
		if override, _ := args["intent_id"].(string); override != "" {
			intentID = override
		}
		tctx.Effects.Out.SourceRun = &tools.SourceRunCapture{
			CheckID:          tctx.Identity.ToolCallID,
			IsCheck:          isCheck,
			Command:          "go test ./...",
			ExitCode:         0,
			Verdict:          verdict,
			SourceRevision:   "rev-1",
			SourceRootDigest: "root-1",
			Cwd:              ".",
			UserIntentID:     intentID,
			WorkflowRunID:    tctx.Turn.WorkflowRunID,
		}
		return "verify ok", nil
	})
	_ = reg.Register("command", func(_ context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if commitThenFail, _ := args["commit_progress_then_fail"].(bool); commitThenFail {
			snap, err := prog.Current(ctx, sess.ID)
			testutil.FailErr(t, "read progress before nested update", err)
			content, _ := args["content"].(string)
			prepared, err := progress.PrepareAuthorUpdate(content)
			testutil.FailErr(t, "prepare nested update", err)
			res, err := prepared.ExpectRevision(snap.Revision).ExpectItemIDs(snap.ItemIDs...).Apply(ctx, prog, sess.ID, nil)
			testutil.FailErr(t, "apply nested update", err)
			if len(res.CompletedItemIDs) > 0 {
				tctx.Effects.Out.MarkPlanItemCompleted()
			}
			return "", errors.New("later operation failed")
		}
		tctx.Effects.Out.SourceRun = &tools.SourceRunCapture{
			CheckID:          tctx.Identity.ToolCallID,
			IsCheck:          false,
			Command:          "echo ok",
			ExitCode:         0,
			Verdict:          api.SourceVerdictPassed,
			SourceRevision:   "rev-1",
			SourceRootDigest: "root-1",
			Cwd:              ".",
			UserIntentID:     tctx.Turn.UserIntentID,
			WorkflowRunID:    tctx.Turn.WorkflowRunID,
		}
		return "ok", nil
	})
	h.reg = reg

	deps := promptloop.StoreDeps(st)
	deps.Model.LLM = h.llm
	deps.Context.Tools = reg
	deps.Context.Policy = &recordingToolPolicy{}
	deps.Context.Limits = loopTestLimits(20)
	deps.Closeout.HintConfig = hints
	deps.Context.BuildMessages = func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
		msgs := []api.Message{{
			ID:        "sys-standing",
			Role:      api.MessageRoleSystem,
			Content:   "standing system preamble",
			Origin:    api.MessageOriginHost,
			Authority: api.ContentAuthoritySystem,
			TrustTier: api.ContentTrustTierTrusted,
		}}
		return append(msgs, history...), nil
	}
	deps.Model.FitRequest = func(_ context.Context, _ *api.Session, _ []api.Message, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
		if _, ok := findCueMessage(req.Messages); ok {
			h.fitSawCue++
		}
		return req, nil
	}
	deps.Closeout.EvaluateContent = func(_ context.Context, _ *api.Session, anchor oar.Anchor, obs oar.ContentObservation, _ string, _ map[string]any) (*guidance.Refusal, bool) {
		if anchor == oar.AnchorContentInput {
			for _, seg := range obs.Segments {
				if seg.Content == hints.ResolveHintMessage(completioncue.CodeOutcomeCoverage) ||
					seg.Content == hints.ResolveHintMessage(completioncue.CodeFinalItemCoverage) {
					h.oarSawCue++
				}
			}
		}
		return nil, false
	}
	if withFeature {
		deps.Context.CompletionCues = h.tracker
		deps.Context.PendingProgressCount = func(ctx context.Context, s *api.Session) (int, bool) {
			snap, err := prog.Current(ctx, s.ID)
			if err != nil || len(snap.Items()) == 0 {
				return 0, false
			}
			return progress.Summarize(snap.Content).Pending, true
		}
		deps.Context.ConfirmCheckCurrent = func(_ context.Context, _ *api.Session, _ completioncue.CheckFact) bool {
			return h.checkCurrent
		}
	}
	h.loop = promptloop.NewPromptLoopForTest(deps)
	return h
}

type completionCueTestStore struct {
	st *storetest.Store
}

func (s completionCueTestStore) GetCompletionCueState(ctx context.Context, sessionID string) (completioncue.DeliveredState, error) {
	st, err := s.st.CompletionCues.GetCompletionCueState(ctx, sessionID)
	if err != nil {
		return completioncue.DeliveredState{}, err
	}
	return completioncue.DeliveredState{
		IntentID:           st.IntentID,
		OutcomeDelivered:   st.OutcomeDelivered,
		FinalItemDelivered: st.FinalItemDelivered,
	}, nil
}

func (s completionCueTestStore) SaveCompletionCueState(ctx context.Context, sessionID string, state completioncue.DeliveredState) error {
	return s.st.CompletionCues.SaveCompletionCueState(ctx, sessionID, stateToStore(state))
}

func stateToStore(state completioncue.DeliveredState) store.CompletionCueState {
	return store.CompletionCueState{
		IntentID:           state.IntentID,
		OutcomeDelivered:   state.OutcomeDelivered,
		FinalItemDelivered: state.FinalItemDelivered,
	}
}

func findCueMessage(messages []api.Message) (api.Message, bool) {
	for _, msg := range messages {
		if msg.ID == "host-plan-completion-cue" {
			return msg, true
		}
	}
	return api.Message{}, false
}

func cueCodesInRequests(reqs []modelcall.CompletionRequest) []string {
	out := make([]string, len(reqs))
	for i, req := range reqs {
		if msg, ok := findCueMessage(req.Messages); ok {
			out[i] = msg.HostSignalID
		}
	}
	return out
}

func progressUpdateArgs(t *testing.T, prog progress.Store, sessionID, content string) map[string]any {
	t.Helper()
	snap, err := prog.Current(t.Context(), sessionID)
	testutil.FailErr(t, "read progress snapshot", err)
	args := map[string]any{"content": content}
	if len(snap.Items()) > 0 {
		args["expected_revision"] = snap.Revision
		args["item_ids"] = snap.ItemIDs
	}
	return args
}

func TestCompletionCueAcceptanceRequestSequenceAndBaselineParity(t *testing.T) {
	ctx := t.Context()

	seedThree := "- [ ] Step 1\n- [ ] Step 2\n- [ ] Step 3"
	closeOne := "- [x] Step 1\n- [ ] Step 2\n- [ ] Step 3"
	closeTwo := "- [x] Step 1\n- [x] Step 2\n- [ ] Step 3"
	closeAll := "- [x] Step 1\n- [x] Step 2\n- [x] Step 3"

	runScript := func(withFeature bool) (*completionCueHarness, []api.Message) {
		var h *completionCueHarness
		steps := []scriptedStep{
			// Decision 0 (initial request): seed 3 pending rows -> no cue on decision 0.
			{toolCalls: []api.ToolCall{{ID: "tc-seed", Name: "update_progress", Args: map[string]any{"content": seedThree}}}},
			// Decision 1 (after seed): ordinary read -> no cue on decision 1.
			{toolCalls: []api.ToolCall{{ID: "tc-read", Name: "read", Args: map[string]any{}}}},
			// Decision 2 (after read): launch-only verify -> no cue on decision 2.
			{toolCalls: []api.ToolCall{{ID: "tc-launch", Name: "verify", Args: map[string]any{"launch_only": true}}}},
			// Decision 3 (after launch): fresh passing check -> no cue on decision 3.
			{toolCalls: []api.ToolCall{{ID: "tc-chk", Name: "verify", Args: map[string]any{}}}},
			// Decision 4 (after passing check with 3 pending): expects PLAN_OUTCOME_COVERAGE.
			{toolCalls: []api.ToolCall{{ID: "tc-read2", Name: "read", Args: map[string]any{}}}},
			// Decision 5 (unrelated decision): no cue. Close item 1 leaving 2 pending.
			{},
			// Decision 6 (after closing item 1 leaving 2 pending, outcome already delivered): no cue. Close item 2 leaving 1 pending.
			{},
			// Decision 7 (after closing item 2 leaving 1 pending): expects PLAN_FINAL_ITEM_COVERAGE.
			{},
			// Decision 8 (after bulk closing last item leaving 0 pending): no cue, finish.
			{content: "All done."},
		}
		h = newCompletionCueHarness(t, withFeature, steps)
		// Wrap LLM to compute current progress revision dynamically for steps 5, 6, 7.
		origLLM := h.llm
		dynamicLLM := &dynamicScriptLLM{
			inner: origLLM,
			beforeStep: func(stepIdx int) {
				switch stepIdx {
				case 5:
					origLLM.steps[5] = scriptedStep{toolCalls: []api.ToolCall{{
						ID: "tc-close1", Name: "update_progress",
						Args: progressUpdateArgs(t, h.progress, h.sess.ID, closeOne),
					}}}
				case 6:
					origLLM.steps[6] = scriptedStep{toolCalls: []api.ToolCall{{
						ID: "tc-close2", Name: "update_progress",
						Args: progressUpdateArgs(t, h.progress, h.sess.ID, closeTwo),
					}}}
				case 7:
					origLLM.steps[7] = scriptedStep{toolCalls: []api.ToolCall{{
						ID: "tc-close3", Name: "update_progress",
						Args: progressUpdateArgs(t, h.progress, h.sess.ID, closeAll),
					}}}
				}
			},
		}
		deps := promptloop.StoreDeps(h.st)
		_ = deps
		// Swap client directly on the harness loop model.
		h.loop.Model.Deps.LLM = dynamicLLM

		// Verify preparation has no cue.
		prepReq, err := h.loop.ProjectPreparation(ctx, h.sess, userHistory("Build feature"), []api.Message{{
			ID: "sys-standing", Role: api.MessageRoleSystem, Content: "standing system preamble",
			Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
		}}, inject.CoordinatorTurnFrame{Machine: inject.Machine{ProfileID: "coordinator"}})
		testutil.FailErr(t, "ProjectPreparation", err)
		if _, ok := findCueMessage(prepReq.Messages); ok {
			t.Fatal("ProjectPreparation must never emit a completion cue")
		}

		_, err = h.loop.Run(ctx, promptloop.PromptRunInput{
			SessionID: h.sess.ID,
			Session:   h.sess,
			History:   userHistory("Build feature"),
			ProfileID: "coordinator",
		})
		testutil.FailErr(t, "loop.Run", err)
		stored, err := h.st.Transcript.GetMessages(ctx, h.sess.ID)
		testutil.FailErr(t, "read transcript", err)
		return h, stored
	}

	baseline, baseTranscript := runScript(false)
	candidate, candTranscript := runScript(true)

	// Zero extra model calls and zero extra transcript rows.
	if len(baseline.llm.requests) != len(candidate.llm.requests) {
		t.Fatalf("model calls baseline=%d candidate=%d", len(baseline.llm.requests), len(candidate.llm.requests))
	}
	if len(baseTranscript) != len(candTranscript) {
		t.Fatalf("transcript rows baseline=%d candidate=%d", len(baseTranscript), len(candTranscript))
	}

	// Exact decision-index cue sequence.
	wantCodes := []string{
		"",                                 // 0: initial
		"",                                 // 1: after seed
		"",                                 // 2: after read
		"",                                 // 3: after launch-only
		completioncue.CodeOutcomeCoverage,  // 4: after passing check (3 pending)
		"",                                 // 5: unrelated decision
		"",                                 // 6: outcome already delivered (2 pending)
		completioncue.CodeFinalItemCoverage, // 7: closed to 1 pending
		"",                                 // 8: closed to 0 pending
	}
	gotCodes := cueCodesInRequests(candidate.llm.requests)
	if !slices.Equal(gotCodes, wantCodes) {
		t.Fatalf("cue sequence = %v, want %v", gotCodes, wantCodes)
	}

	// Assert guidance authority, message text from registered OAR hint, and byte-identical standing preamble.
	for _, idx := range []int{4, 7} {
		req := candidate.llm.requests[idx]
		cue, ok := findCueMessage(req.Messages)
		if !ok {
			t.Fatalf("decision %d missing cue message", idx)
		}
		if cue.Role != api.MessageRoleSystem || cue.Origin != api.MessageOriginHost ||
			cue.Authority != api.ContentAuthoritySystem || cue.TrustTier != api.ContentTrustTierTrusted {
			t.Fatalf("decision %d unexpected cue provenance: %+v", idx, cue)
		}
		wantText := candidate.hints.ResolveHintMessage(wantCodes[idx])
		if cue.Content != wantText {
			t.Fatalf("decision %d cue content = %q, want %q", idx, cue.Content, wantText)
		}
		// Cue must follow result history (last message in request).
		if req.Messages[len(req.Messages)-1].ID != cue.ID {
			t.Fatalf("decision %d cue must follow result history", idx)
		}
		// Standing preamble byte-identical with baseline.
		basePre := baseline.llm.requests[idx].Messages[:providerwire.SystemPreambleEnd(baseline.llm.requests[idx].Messages)]
		candPre := req.Messages[:providerwire.SystemPreambleEnd(req.Messages)]
		baseJSON, _ := json.Marshal(basePre)
		candJSON, _ := json.Marshal(candPre)
		if string(baseJSON) != string(candJSON) {
			t.Fatalf("decision %d standing preamble drifted:\nbase=%s\ncand=%s", idx, baseJSON, candJSON)
		}
	}

	// OAR AnchorContentInput and FitRequest must both see the candidate cue messages.
	if candidate.oarSawCue != 2 {
		t.Fatalf("OAR AnchorContentInput saw %d cues, want 2", candidate.oarSawCue)
	}
	if candidate.fitSawCue != 2 {
		t.Fatalf("FitRequest saw %d cues, want 2", candidate.fitSawCue)
	}
}

func TestCompletionCueNestedProgramFailurePreservesCommittedProgress(t *testing.T) {
	ctx := t.Context()
	seedTwo := "- [ ] Step 1\n- [ ] Step 2"
	closeOne := "- [x] Step 1\n- [ ] Step 2"

	var h *completionCueHarness
	steps := []scriptedStep{
		{toolCalls: []api.ToolCall{{ID: "tc-seed", Name: "update_progress", Args: map[string]any{"content": seedTwo}}}},
		{toolCalls: []api.ToolCall{{ID: "tc-prog", Name: "command", Args: map[string]any{"commit_progress_then_fail": true, "content": closeOne}}}},
		{content: "handled failure"},
	}
	h = newCompletionCueHarness(t, true, steps)
	_, err := h.loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: h.sess.ID,
		Session:   h.sess,
		History:   userHistory("Run program"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run", err)

	gotCodes := cueCodesInRequests(h.llm.requests)
	wantCodes := []string{"", "", completioncue.CodeFinalItemCoverage}
	if !slices.Equal(gotCodes, wantCodes) {
		t.Fatalf("cue sequence = %v, want %v", gotCodes, wantCodes)
	}
	// Verify failure was preserved in tool result.
	stored, err := h.st.Transcript.GetMessages(ctx, h.sess.ID)
	testutil.FailErr(t, "read transcript", err)
	sawError := false
	for _, msg := range stored {
		if msg.Role == api.MessageRoleTool && msg.ToolResult != nil && msg.ToolResult.Tool == "command" && msg.ToolResult.Outcome == api.ToolResultOutcomeError {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected failed command outcome to be preserved alongside committed progress")
	}
}

func TestCompletionCueIneligibleEventsProduceNoCue(t *testing.T) {
	ctx := t.Context()
	seedTwo := "- [ ] Step 1\n- [ ] Step 2"
	descopeOne := "- [-] Step 1\n- [ ] Step 2"
	renameOne := "- [-] Step 1 renamed\n- [ ] Step 2"

	var h *completionCueHarness
	steps := []scriptedStep{
		{toolCalls: []api.ToolCall{{ID: "tc-seed", Name: "update_progress", Args: map[string]any{"content": seedTwo}}}},
		{}, // descope only
		{}, // rename only
		{toolCalls: []api.ToolCall{{ID: "tc-cmd", Name: "command", Args: map[string]any{}}}},                              // ordinary command (is_check=false)
		{toolCalls: []api.ToolCall{{ID: "tc-fail", Name: "verify", Args: map[string]any{"fail": true}}}},                   // failed check
		{toolCalls: []api.ToolCall{{ID: "tc-old", Name: "verify", Args: map[string]any{"intent_id": "other-intent"}}}},     // unrelated intent
		{toolCalls: []api.ToolCall{{ID: "tc-stale", Name: "verify", Args: map[string]any{}}}},                              // stale source
		{content: "done"},
	}
	h = newCompletionCueHarness(t, true, steps)
	origLLM := h.llm
	h.loop.Model.Deps.LLM = &dynamicScriptLLM{
		inner: origLLM,
		beforeStep: func(stepIdx int) {
			switch stepIdx {
			case 1:
				origLLM.steps[1] = scriptedStep{toolCalls: []api.ToolCall{{
					ID: "tc-descope", Name: "update_progress",
					Args: progressUpdateArgs(t, h.progress, h.sess.ID, descopeOne),
				}}}
			case 2:
				origLLM.steps[2] = scriptedStep{toolCalls: []api.ToolCall{{
					ID: "tc-rename", Name: "update_progress",
					Args: progressUpdateArgs(t, h.progress, h.sess.ID, renameOne),
				}}}
			case 6:
				h.checkCurrent = false
			}
		},
	}

	_, err := h.loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: h.sess.ID,
		Session:   h.sess,
		History:   userHistory("Check ineligible events"),
		ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run", err)

	for i, code := range cueCodesInRequests(h.llm.requests) {
		if code != "" {
			t.Fatalf("decision %d unexpectedly emitted cue %q", i, code)
		}
	}
}

func TestCompletionCueOneItemPlanCheckVsInspectionOnly(t *testing.T) {
	ctx := t.Context()
	seedOne := "- [ ] Verify all paths"
	doneOne := "- [x] Verify all paths"

	// Case A: one-item plan, passing check while item open -> PLAN_FINAL_ITEM_COVERAGE before final write.
	var hA *completionCueHarness
	stepsA := []scriptedStep{
		{toolCalls: []api.ToolCall{{ID: "tc-seed", Name: "update_progress", Args: map[string]any{"content": seedOne}}}},
		{toolCalls: []api.ToolCall{{ID: "tc-chk", Name: "verify", Args: map[string]any{}}}},
		{}, // final checklist write
		{content: "done"},
	}
	hA = newCompletionCueHarness(t, true, stepsA)
	origA := hA.llm
	hA.loop.Model.Deps.LLM = &dynamicScriptLLM{
		inner: origA,
		beforeStep: func(stepIdx int) {
			if stepIdx == 2 {
				origA.steps[2] = scriptedStep{toolCalls: []api.ToolCall{{
					ID: "tc-done", Name: "update_progress",
					Args: progressUpdateArgs(t, hA.progress, hA.sess.ID, doneOne),
				}}}
			}
		},
	}
	_, err := hA.loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: hA.sess.ID, Session: hA.sess, History: userHistory("One item with check"), ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run case A", err)
	wantA := []string{"", "", completioncue.CodeFinalItemCoverage, ""}
	if got := cueCodesInRequests(hA.llm.requests); !slices.Equal(got, wantA) {
		t.Fatalf("case A cues = %v, want %v", got, wantA)
	}

	// Case B: one-item plan, inspection only, final checklist write -> None.
	var hB *completionCueHarness
	stepsB := []scriptedStep{
		{toolCalls: []api.ToolCall{{ID: "tc-seed", Name: "update_progress", Args: map[string]any{"content": seedOne}}}},
		{toolCalls: []api.ToolCall{{ID: "tc-read", Name: "read", Args: map[string]any{}}}},
		{}, // final checklist write
		{content: "done"},
	}
	hB = newCompletionCueHarness(t, true, stepsB)
	origB := hB.llm
	hB.loop.Model.Deps.LLM = &dynamicScriptLLM{
		inner: origB,
		beforeStep: func(stepIdx int) {
			if stepIdx == 2 {
				origB.steps[2] = scriptedStep{toolCalls: []api.ToolCall{{
					ID: "tc-done", Name: "update_progress",
					Args: progressUpdateArgs(t, hB.progress, hB.sess.ID, doneOne),
				}}}
			}
		},
	}
	_, err = hB.loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: hB.sess.ID, Session: hB.sess, History: userHistory("One item inspection only"), ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run case B", err)
	for i, code := range cueCodesInRequests(hB.llm.requests) {
		if code != "" {
			t.Fatalf("case B decision %d unexpectedly emitted %q", i, code)
		}
	}
}

func TestCompletionCueProseFinishForcedCloseoutSuppressesCue(t *testing.T) {
	ctx := t.Context()
	h := newCompletionCueHarness(t, true, []scriptedStep{{content: "forced final answer"}})
	_, err := progress.PrepareAuthorUpdate("- [ ] Step 1\n- [ ] Step 2")
	testutil.FailErr(t, "prepare", err)
	_, err = h.progress.Update(ctx, h.sess.ID, func(progress.Snapshot) (progress.Document, error) {
		return progress.Document{Content: "- [ ] Step 1\n- [ ] Step 2"}, nil
	})
	testutil.FailErr(t, "seed progress", err)
	hist := userHistory("Forced closeout")
	h.tracker.RecordPlanItemCompleted(h.sess.ID, "", hist[0].ID)

	_, err = h.loop.Run(ctx, promptloop.PromptRunInput{
		SessionID:   h.sess.ID,
		Session:     h.sess,
		History:     hist,
		ProfileID:   "coordinator",
		ProseFinish: true,
	})
	testutil.FailErr(t, "loop.Run forced", err)
	if len(h.llm.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(h.llm.requests))
	}
	if _, ok := findCueMessage(h.llm.requests[0].Messages); ok {
		t.Fatal("forced final answer must not deliver a completion cue")
	}
}

type dynamicScriptLLM struct {
	inner      *capturingScriptLLM
	beforeStep func(stepIdx int)
}

func (d *dynamicScriptLLM) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if d.beforeStep != nil {
		d.beforeStep(d.inner.idx)
	}
	return d.inner.Complete(ctx, req)
}

func (d *dynamicScriptLLM) Stream(ctx context.Context, req modelcall.CompletionRequest, cb func(modelcall.StreamEvent)) (*modelcall.Completion, error) {
	if d.beforeStep != nil {
		d.beforeStep(d.inner.idx)
	}
	return d.inner.Stream(ctx, req, cb)
}

func TestCompletionCueRebuildStability(t *testing.T) {
	ctx := t.Context()
	// Verify that on a retryable stream error, the rebuilt request emits the exact same cue bytes.
	seedThree := "- [ ] Step 1\n- [ ] Step 2\n- [ ] Step 3"
	steps := []scriptedStep{
		{toolCalls: []api.ToolCall{{ID: "tc-seed", Name: "update_progress", Args: map[string]any{"content": seedThree}}}},
		{toolCalls: []api.ToolCall{{ID: "tc-chk", Name: "verify", Args: map[string]any{}}}},
		{content: "done"},
	}
	h := newCompletionCueHarness(t, true, steps)
	_, err := h.loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: h.sess.ID, Session: h.sess, History: userHistory("Rebuild stability"), ProfileID: "coordinator",
	})
	testutil.FailErr(t, "loop.Run", err)

	// Confirm the check decision delivered PLAN_OUTCOME_COVERAGE.
	if got := cueCodesInRequests(h.llm.requests); !slices.Equal(got, []string{"", "", completioncue.CodeOutcomeCoverage}) {
		t.Fatalf("cues = %v", got)
	}
	// Confirm the cue text is non-empty and matches the registered OAR hint.
	cueMsg, ok := findCueMessage(h.llm.requests[2].Messages)
	if !ok {
		t.Fatal("missing cue on decision 2")
	}
	wantText := h.hints.ResolveHintMessage(completioncue.CodeOutcomeCoverage)
	if strings.TrimSpace(cueMsg.Content) != wantText {
		t.Fatalf("cue content = %q, want %q", cueMsg.Content, wantText)
	}
}
