package promptloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/loopguard"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// alwaysSameToolClient repeats a blocked call until closeout is requested.
type alwaysSameToolClient struct {
	closeout bool
	// ignoreCloseout keeps calling the tool on the final, tool-less turn.
	ignoreCloseout bool
	calls          int
	args           map[string]any
	iterations     []int
	toolCounts     []int
}

func (c *alwaysSameToolClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	c.iterations = append(c.iterations, req.Debug.Iteration)
	c.toolCounts = append(c.toolCounts, len(req.Tools))
	if !c.ignoreCloseout && (c.closeout || !req.ToolsCallable()) {
		return &modelcall.Completion{Content: "Stopping: the same call keeps being blocked."}, nil
	}
	return &modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read", Args: c.args}}}, nil
}

func (c *alwaysSameToolClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := c.Complete(ctx, req)
		if err != nil {
			return
		}
		ch <- modelcall.StreamChunk{Content: completion.Content, ToolCalls: completion.ToolCalls, Done: true}
	}()
	return ch, nil
}

func TestBlockedLoopClosesOutInsteadOfSpinning(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{"path": "README.md"}
	guard := loopguard.NewMemoryDoomLoopGuard()
	msgStore := store.NewMemory()
	sess, err := msgStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	// Put the guard past its threshold so every attempt is blocked from the start.
	for i := 0; i < loopguard.DoomLoopMaxAttempts; i++ {
		testutil.FailErr(t, "seed doom loop attempt", guard.RecordAttempt(ctx, sess.ID, uuid.NewString(), "read", args, "", false))
	}

	fmttr := guidance.NewStaticRejectFormatter(&guidance.HintConfig{
		HintCodes: map[string]guidance.HintEntry{
			"DOOM_LOOP_REPEAT": {Message: "blocked repeat"},
		},
	})
	client := &alwaysSameToolClient{args: args}
	deps := promptloop.StoreDeps(msgStore)
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	deps.DoomLoop = guard
	deps.RejectFmt = fmttr
	deps.FormatDoomLoopReject = func(_ context.Context, _, tool string, _ map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
		data := map[string]any{"count": count, "tool": tool}
		if repeatedCode != "" {
			data["code"] = repeatedCode
		}
		block, err := fmttr.Format("DOOM_LOOP_REPEAT", data)
		if err != nil {
			return nil, err
		}
		return guidance.NewRefusal("DOOM_LOOP_REPEAT", block), nil
	}
	var closeout promptloop.TurnCloseoutCause
	deps.TurnCloseoutNudge = func(_ context.Context, _ *api.Session, _ string, cause promptloop.TurnCloseoutCause) promptloop.HostNudge {
		client.closeout = true
		closeout = cause
		return promptloop.HostNudge{Content: "final turn: " + cause.Text()}
	}

	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run", err)
	if result.LastAssistantContent != "Stopping: the same call keeps being blocked." {
		t.Fatalf("closeout content = %q", result.LastAssistantContent)
	}
	// The closing instruction names the refused call, not a category.
	if closeout.Reason != promptloop.TurnCloseoutBlockedLoop || closeout.BlockedTool != "read" || closeout.BlockedCode != "DOOM_LOOP_REPEAT" || closeout.BlockedCount < promptloop.BlockedLoopRejectCap {
		t.Fatalf("closeout cause = %+v", closeout)
	}
	if want := "`read` was refused " + strconv.Itoa(closeout.BlockedCount) + " times in a row (DOOM_LOOP_REPEAT)"; closeout.Text() != want {
		t.Fatalf("closeout text = %q, want %q", closeout.Text(), want)
	}

	// One blocked batch introduces the Code and is free, the cap bounds the
	// repeats after it, and one further request is the prose closeout.
	maxCalls := promptloop.BlockedLoopRejectCap + 2
	if client.calls > maxCalls {
		t.Fatalf("made %d model requests for a permanently blocked call; cap is %d repeats after the first",
			client.calls, promptloop.BlockedLoopRejectCap)
	}
	if client.calls < 2 {
		t.Fatalf("made only %d model requests; the loop should retry a few times first", client.calls)
	}
	last := client.calls - 1
	if last < 0 || client.toolCounts[last] == 0 {
		t.Fatalf("closeout tools = %v want schemas kept on the trailing turn", client.toolCounts)
	}
	if client.iterations[last] != client.calls {
		t.Fatalf("closeout iteration = %d want %d", client.iterations[last], client.calls)
	}
	if client.iterations[last] >= 500 {
		t.Fatalf("closeout iteration = %v want current step", client.iterations)
	}
}

// A final turn answered with a tool call is the model's miss: the host names
// the call and why the turn had ended, instead of reporting a silent provider.
func TestBlockedLoopFinalTurnToolCallIsReportedAsTheModelsMiss(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{"path": "a.go"}
	guard := loopguard.NewMemoryDoomLoopGuard()
	msgStore := store.NewMemory()
	sess, err := msgStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)
	for i := 0; i < loopguard.DoomLoopMaxAttempts; i++ {
		testutil.FailErr(t, "seed doom loop attempt", guard.RecordAttempt(ctx, sess.ID, uuid.NewString(), "read", args, "", false))
	}
	fmttr := guidance.NewStaticRejectFormatter(&guidance.HintConfig{HintCodes: map[string]guidance.HintEntry{"DOOM_LOOP_REPEAT": {Message: "blocked repeat"}}})
	client := &alwaysSameToolClient{args: args, ignoreCloseout: true}
	deps := promptloop.StoreDeps(msgStore)
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	deps.DoomLoop = guard
	deps.RejectFmt = fmttr
	deps.FormatDoomLoopReject = func(_ context.Context, _, tool string, _ map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
		block, err := fmttr.Format("DOOM_LOOP_REPEAT", map[string]any{"count": count, "tool": tool, "code": repeatedCode})
		if err != nil {
			return nil, err
		}
		return guidance.NewRefusal("DOOM_LOOP_REPEAT", block), nil
	}
	deps.TurnCloseoutNudge = func(_ context.Context, _ *api.Session, _ string, cause promptloop.TurnCloseoutCause) promptloop.HostNudge {
		return promptloop.HostNudge{Content: "final turn: " + cause.Text()}
	}
	loop := promptloop.NewPromptLoopForTest(deps)
	_, err = loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID, Session: sess, History: userHistory("go"), ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	var miss *promptloop.ProseTurnToolCallError
	if !errors.As(err, &miss) {
		t.Fatalf("final-turn tool call reported as %v, want ProseTurnToolCallError", err)
	}
	if len(miss.Tools) != 1 || miss.Tools[0] != "read" || !strings.Contains(miss.CloseoutReason, "`read` was refused") {
		t.Fatalf("miss = %+v", miss)
	}
	if _, empty := failure.AsProviderEmptyCompletion(err); empty {
		t.Fatal("the model's miss was attributed to the provider")
	}
}

// Finish-blocked early closeout after a doom loop commits assembled synthesis.
func TestBlockedLoopEarlyCloseoutAssemblesWhenFinishBlocked(t *testing.T) {
	ctx := context.Background()
	args := map[string]any{"path": "README.md"}
	guard := loopguard.NewMemoryDoomLoopGuard()
	msgStore := store.NewMemory()
	sess, err := msgStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	for i := 0; i < loopguard.DoomLoopMaxAttempts; i++ {
		testutil.FailErr(t, "seed doom loop attempt", guard.RecordAttempt(ctx, sess.ID, uuid.NewString(), "read", args, "", false))
	}

	fmttr := guidance.NewStaticRejectFormatter(&guidance.HintConfig{
		HintCodes: map[string]guidance.HintEntry{
			"DOOM_LOOP_REPEAT": {Message: "blocked repeat"},
		},
	})
	client := &alwaysSameToolClient{args: args}
	clientWithSynth := &closeoutSynthesisClient{inner: client, synthesis: "## Status\nStopped after repeated blocks."}

	deps := promptloop.StoreDeps(msgStore)
	deps.LLM = clientWithSynth
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	deps.DoomLoop = guard
	deps.RejectFmt = fmttr
	deps.FormatDoomLoopReject = func(_ context.Context, _, tool string, _ map[string]any, count int, repeatedCode string) (*guidance.Refusal, error) {
		data := map[string]any{"count": count, "tool": tool}
		if repeatedCode != "" {
			data["code"] = repeatedCode
		}
		block, err := fmttr.Format("DOOM_LOOP_REPEAT", data)
		if err != nil {
			return nil, err
		}
		return guidance.NewRefusal("DOOM_LOOP_REPEAT", block), nil
	}
	deps.TurnCloseoutNudge = func(_ context.Context, _ *api.Session, _ string, cause promptloop.TurnCloseoutCause) promptloop.HostNudge {
		return promptloop.HostNudge{Content: "final turn: " + cause.Text()}
	}
	deps.BeforeFinishNoToolTurn = func(_ context.Context, _ *api.Session, _ []api.Message, _, _, _ string, _ bool, _ []string, _ bool) (*guidance.Refusal, bool) {
		return guidance.NewRefusal("PROGRESS_OPEN_BEFORE_CLOSEOUT", "Rejected: held\n\nCode: PROGRESS_OPEN_BEFORE_CLOSEOUT\n"), true
	}
	deps.AssembleLedgerCloseout = func(_ context.Context, _, _ string, _ []string, drafted string, _ int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
		return guidance.CoordinatorCompletionReport{Synthesis: drafted},
			&api.CitationGrounding{HostAssembled: true, Traced: false}
	}
	deps.PromptTurnSurface = func(string) string { return "implement_investigate" }

	loop := promptloop.NewPromptLoopForTest(deps)
	res, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run", err)
	if res == nil || res.LastAssistantID == "" {
		t.Fatal("expected committed closeout")
	}
	if !strings.Contains(res.LastAssistantContent, "Stopped after repeated blocks") {
		t.Fatalf("assembled closeout missing drafted synthesis, got %q", res.LastAssistantContent)
	}
}

// closeoutSynthesisClient emits a JSON synthesis envelope when tools are omitted.
type closeoutSynthesisClient struct {
	inner     *alwaysSameToolClient
	synthesis string
}

func (c *closeoutSynthesisClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return c.inner.Complete(ctx, req)
}

func (c *closeoutSynthesisClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		if !req.ToolsCallable() || c.inner.calls >= promptloop.BlockedLoopRejectCap {
			c.inner.calls++
			body := "```json\n{\"synthesis\":" + quoteJSON(c.synthesis) + "}\n```"
			ch <- modelcall.StreamChunk{Content: body, Done: true}
			return
		}
		innerCh, err := c.inner.Stream(ctx, req)
		if err != nil {
			return
		}
		for chunk := range innerCh {
			ch <- chunk
		}
	}()
	return ch, nil
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The streak resets when a batch actually runs, so an occasional block during real
// progress never accumulates into a false termination.
func TestBlockedLoopStreakResetsOnProgress(t *testing.T) {
	if promptloop.BlockedLoopRejectCap < 2 {
		t.Skip("cap too small for this scenario")
	}
	ctx := context.Background()
	msgStore := store.NewMemory()
	sess, err := msgStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	// No doom-loop guard: every batch runs, so the streak never advances and the
	// turn ends on its own prose rather than a blocked-loop closeout.
	client := &sequentialLLMClient{completions: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read", Args: map[string]any{"path": "a.md"}}}},
		{ToolCalls: []api.ToolCall{{ID: "tc2", Name: "read", Args: map[string]any{"path": "b.md"}}}},
		{Content: "Read both files."},
	}}
	deps := promptloop.StoreDeps(msgStore)
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}

	loop := promptloop.NewPromptLoopForTest(deps)
	res, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run", err)
	if res == nil {
		t.Fatal("nil run result")
	}
	if !strings.Contains(res.LastAssistantContent, "Read both files") {
		t.Fatalf("turn should have finished on its own prose, got %q", res.LastAssistantContent)
	}
}

// Schema rejection before the subsystem owner does not close the turn.
func TestSchemaRejectsDoNotForceBlockedLoopCloseout(t *testing.T) {
	ctx := context.Background()
	msgStore := store.NewMemory()
	sess, err := msgStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	reg := tools.NewStubRegistry()
	reg.SetFail("read", guidance.NewRefusal("TOOL_ARGS_INVALID",
		"Rejected: missing path\n\nCode: TOOL_ARGS_INVALID\n"))

	client := &schemaRejectThenProseClient{
		args:       map[string]any{"filepath": "ntp_check.py"},
		proseAfter: promptloop.BlockedLoopRejectCap + 1,
	}
	deps := promptloop.StoreDeps(msgStore)
	deps.LLM = client
	deps.Tools = reg
	deps.Policy = &recordingToolPolicy{}
	deps.TurnCloseoutNudge = func(_ context.Context, _ *api.Session, _ string, cause promptloop.TurnCloseoutCause) promptloop.HostNudge {
		return promptloop.HostNudge{Content: "forced closeout: " + cause.Text()}
	}

	loop := promptloop.NewPromptLoopForTest(deps)
	res, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("fix the port"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run", err)
	if res == nil {
		t.Fatal("nil run result")
	}
	if strings.Contains(res.LastAssistantContent, "forced closeout") {
		t.Fatalf("schema rejects tripped blocked-loop closeout: %q", res.LastAssistantContent)
	}
	if !strings.Contains(res.LastAssistantContent, "Need a different argument shape") {
		t.Fatalf("turn should have continued past the reject cap, got %q", res.LastAssistantContent)
	}
	if client.calls <= promptloop.BlockedLoopRejectCap {
		t.Fatalf("made %d model requests; want more than the blocked-loop cap %d",
			client.calls, promptloop.BlockedLoopRejectCap)
	}
}

// schemaRejectThenProseClient repeats one invalid call, then answers in prose.
type schemaRejectThenProseClient struct {
	calls      int
	args       map[string]any
	proseAfter int
}

func (c *schemaRejectThenProseClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !req.ToolsCallable() {
		return &modelcall.Completion{Content: "forced closeout received"}, nil
	}
	c.calls++
	if c.calls > c.proseAfter {
		return &modelcall.Completion{Content: "Need a different argument shape."}, nil
	}
	return &modelcall.Completion{ToolCalls: []api.ToolCall{{ID: "tc1", Name: "read", Args: c.args}}}, nil
}

func (c *schemaRejectThenProseClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := c.Complete(ctx, req)
		if err != nil {
			return
		}
		if len(completion.ToolCalls) > 0 {
			ch <- modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Done: true}
			return
		}
		ch <- modelcall.StreamChunk{Content: completion.Content, Done: true}
	}()
	return ch, nil
}

func TestPreInvokeRejectAccruesCodeTotalAndEscalates(t *testing.T) {
	// A pre-invoke gate refusal must feed the same per-Code doom-loop accounting
	// as an invoked rejection.
	ctx := context.Background()
	guard := loopguard.NewMemoryDoomLoopGuard()
	msgStore := store.NewMemory()
	sess, err := msgStore.Create(ctx, api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	// Vary args every call — the way a model rewords a brief after each reject —
	// so only the args-independent per-Code counter can accumulate.
	client := &varyingArgsToolClient{}
	var escalateCalls int
	deps := promptloop.StoreDeps(msgStore)
	deps.LLM = client
	deps.Tools = tools.NewStubRegistry()
	deps.Policy = &recordingToolPolicy{}
	deps.DoomLoop = guard
	deps.RejectFmt = guidance.NewStaticRejectFormatter(&guidance.HintConfig{
		HintCodes: map[string]guidance.HintEntry{
			"PROGRESS_ITEM_NOT_CLOSED": {Message: "reconcile the checklist"},
		},
	})
	deps.BeforeToolRun = func(context.Context, *api.Session, []api.Message, string, string, map[string]any) (string, bool, error) {
		return "", false, guidance.NewRefusal("PROGRESS_ITEM_NOT_CLOSED", "Rejected: reconcile the checklist first")
	}
	deps.EscalateRepeatedCode = func(_ context.Context, sessionID, tool string, original *guidance.Refusal) *guidance.Refusal {
		code := original.Code()
		if guard.CodeRejectResponses(sessionID, tool, code) < loopguard.DoomLoopMaxCodeRepeats {
			return nil
		}
		escalateCalls++
		return guidance.NewRefusal("DOOM_LOOP_CODE_REPEAT", "Escalated: this exact guidance keeps failing to land")
	}
	deps.TurnCloseoutNudge = func(_ context.Context, _ *api.Session, _ string, cause promptloop.TurnCloseoutCause) promptloop.HostNudge {
		client.closeout = true
		return promptloop.HostNudge{Content: "final turn: " + cause.Text()}
	}

	loop := promptloop.NewPromptLoopForTest(deps)
	result, err := loop.Run(ctx, promptloop.PromptRunInput{
		SessionID: sess.ID,
		Session:   sess,
		History:   userHistory("go"),
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
	})
	testutil.FailErr(t, "loop.Run", err)
	if result.LastAssistantContent != "Stopping: the same call keeps being blocked." {
		t.Fatalf("closeout content = %q", result.LastAssistantContent)
	}

	if total := guard.CodeRejectResponses(sess.ID, "read", "PROGRESS_ITEM_NOT_CLOSED"); total < loopguard.DoomLoopMaxCodeRepeats {
		t.Fatalf("code reject total = %d want >= %d — pre-invoke refusals must accrue", total, loopguard.DoomLoopMaxCodeRepeats)
	}
	if escalateCalls == 0 {
		t.Fatal("expected the per-Code escalation to fire for repeated pre-invoke rejects")
	}
	msgs, err := msgStore.GetMessages(ctx, sess.ID)
	testutil.FailErr(t, "get messages", err)
	sawEscalation := false
	for _, m := range msgs {
		if strings.Contains(m.Content, "Escalated: this exact guidance keeps failing to land") {
			sawEscalation = true
			break
		}
	}
	if !sawEscalation {
		t.Fatal("escalated body must replace the verbatim reject in the transcript")
	}
}

// varyingArgsToolClient rewords the same tool call every iteration, the way a
// model does when it misreads a structural reject as an args problem.
type varyingArgsToolClient struct {
	closeout  bool
	calls     int
	batchSize int
	stopAfter int
}

func (c *varyingArgsToolClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.calls++
	if c.closeout || !req.ToolsCallable() || (c.stopAfter > 0 && c.calls > c.stopAfter) {
		return &modelcall.Completion{Content: "Stopping: the same call keeps being blocked."}, nil
	}
	calls := make([]api.ToolCall, max(1, c.batchSize))
	for i := range calls {
		calls[i] = api.ToolCall{ID: uuid.NewString(), Name: "read", Args: map[string]any{"path": "README.md", "attempt": c.calls, "item": i}}
	}
	return &modelcall.Completion{ToolCalls: calls}, nil
}

func (c *varyingArgsToolClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := c.Complete(ctx, req)
		if err != nil {
			return
		}
		ch <- modelcall.StreamChunk{Content: completion.Content, ToolCalls: completion.ToolCalls, Done: true}
	}()
	return ch, nil
}
