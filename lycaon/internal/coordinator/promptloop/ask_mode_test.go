package promptloop

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskFinalPlanClampsLoadedAndImpliedCalls(t *testing.T) {
	metas := []tools.ToolMeta{
		{Name: "command"}, {Name: "command_output"}, {Name: "command_stop"}, {Name: "request_tools"},
		{Name: "verify"}, {Name: "code_rewrite"}, {Name: "promote_overlay"},
		{Name: "mcp_doc_lookup", Source: tools.ToolSourceMCP, ReadOnlyHint: true},
		{Name: "mcp_doc_update", Source: tools.ToolSourceMCP},
		{Name: "workflow_advance"}, {Name: "unknown_dynamic"},
	}
	loop := NewPromptLoopForTest(PromptLoopDeps{Context: ContextDeps{
		Policy: registryTestPolicy{metas: metas},
		LoadedTools: func(string) map[string]bool {
			return map[string]bool{
				"verify": true, "code_rewrite": true, "promote_overlay": true,
				"mcp_doc_lookup": true, "mcp_doc_update": true, "unknown_dynamic": true,
			}
		},
		LiveResources: func(string) toolcontract.ResourcePresence { return toolcontract.ResourcePresence{CommandJobs: true} },
	}})
	for _, posture := range []api.SessionPosture{api.SessionPostureSpec, api.SessionPostureBuild, api.SessionPostureOrchestrate, api.SessionPostureVet} {
		sess := &api.Session{ID: "session", Mode: api.SessionModeAsk, Posture: posture, WorkspacePath: "/project", AgentType: "coordinator"}
		frame := inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{AdvanceWhenGateMet: "coordinator"}}
		meta, plan := mustCoordinatorToolsForTurn(t, loop, sess, frame)
		for _, name := range []string{"command", "command_output", "command_stop", "mcp_doc_lookup", "request_tools", "workflow_advance"} {
			if !containsMeta(meta, name) {
				t.Errorf("%s: missing %s", posture, name)
			}
		}
		for _, name := range []string{"verify", "code_rewrite", "promote_overlay", "mcp_doc_update", "unknown_dynamic"} {
			if plan.Addressable(name) {
				t.Errorf("%s: prohibited %s is addressable", posture, name)
			}
		}
	}
}

func TestRoundAuthorityKeepsWorkerScopeAcrossUpgradeAndFailsClosed(t *testing.T) {
	root := store.RootAuthority{RootSessionID: "root", Mode: api.SessionModeAsk, Started: true}
	var readErr error
	prompt := &promptContext{Deps: ContextDeps{ResolveEffectiveMode: func(context.Context, string) (store.RootAuthority, error) {
		return root, readErr
	}}}
	worker := &api.Session{ID: "worker", ParentSessionID: "root", Mode: api.SessionModeAsk}
	ask, err := prompt.roundAuthority(t.Context(), worker, true)
	testutil.FailErr(t, "read Ask round", err)
	if tc := ask.toolContext(tools.ToolContext{}); !tc.Source.AskMode || !tc.Source.ReadOnly || tc.Identity.RootSessionID != "root" {
		t.Fatalf("Ask round flags = %+v", tc.Source)
	}
	root.Mode = api.SessionModeAgent
	agent, err := prompt.roundAuthority(t.Context(), worker, true)
	testutil.FailErr(t, "read Agent round", err)
	if tc := agent.toolContext(tools.ToolContext{}); tc.Source.AskMode || !tc.Source.ReadOnly {
		t.Fatalf("Agent worker flags = %+v", tc.Source)
	}
	readErr = errors.New("root unavailable")
	if _, err := prompt.roundAuthority(t.Context(), worker, true); !errors.Is(err, readErr) {
		t.Fatalf("round authority error = %v", err)
	}
	unwired := &promptContext{}
	if _, err := unwired.roundAuthority(t.Context(), worker, true); !errors.Is(err, store.ErrSessionModeUnavailable) {
		t.Fatalf("round ran with no authority resolver: %v", err)
	}
	readErr = nil
	root.Mode = api.SessionMode("corrupt")
	if _, err := prompt.roundAuthority(t.Context(), worker, true); !errors.Is(err, store.ErrSessionModeUnavailable) {
		t.Fatalf("corrupt authority was accepted: %v", err)
	}
}

type askModeTestLLMClient struct {
	requests *[][]api.Message
}

func (c *askModeTestLLMClient) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if c.requests != nil {
		*c.requests = append(*c.requests, append([]api.Message(nil), req.Messages...))
	}
	return &modelcall.Completion{Content: "Agent turn completed."}, nil
}

func (c *askModeTestLLMClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	comp, err := c.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 1)
	ch <- modelcall.StreamChunk{Content: comp.Content, Done: true}
	close(ch)
	return ch, nil
}

func TestBetweenTurnUpgradeNoticeSurvivesSQLReopenAndFailedAppendRetry(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "upgrade-notice.db")

	// 1. Create and start an Ask session, then upgrade it to Agent between turns and close the DB.
	sqlDB1 := testdbfixture.OpenPath(t, dbPath)
	testdbseed.InsertProjectRoot(t, sqlDB1, testdbseed.DefaultProjectID, dir)
	ctx = testdbseed.OwnerCaller(t, ctx, sqlDB1)
	st1 := store.NewSQL(sqlDB1)
	ask := api.SessionModeAsk
	sess, err := st1.Create(ctx, api.CreateSessionRequest{Mode: &ask, Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create Ask session", err)
	_, err = st1.BeginTurn(ctx, store.TurnStart{SessionID: sess.ID, ProjectID: sess.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "latch Ask turn start", err)
	testutil.FailErr(t, "append turn 1 messages", st1.AppendMessages(ctx, sess.ID,
		api.Message{ID: "u1", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "inspect in Ask"},
		api.Message{ID: "a1", Role: api.MessageRoleAssistant, Origin: api.MessageOriginModel, Content: "Ask reply"},
	))
	_, err = st1.SetMode(ctx, sess.ID, api.SessionModeAgent)
	testutil.FailErr(t, "upgrade Ask -> Agent between turns", err)

	// Also create an ordinary Agent session (switched from Ask before starting) to verify it never gets a notice.
	ordinary, err := st1.Create(ctx, api.CreateSessionRequest{Mode: &ask, Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create unstarted Ask session", err)
	_, err = st1.SetMode(ctx, ordinary.ID, api.SessionModeAgent)
	testutil.FailErr(t, "switch unstarted session to Agent", err)
	_, err = st1.BeginTurn(ctx, store.TurnStart{SessionID: ordinary.ID, ProjectID: ordinary.ProjectID, Origin: store.TurnOriginUser, InputJSON: "{}"})
	testutil.FailErr(t, "start ordinary Agent turn", err)

	testutil.FailErr(t, "close store before next turn", sqlDB1.Close())

	// 2. Reopen SQL store on the same DB file (simulating host restart between PATCH and the next turn).
	sqlDB2 := testdbfixture.OpenPath(t, dbPath)
	ctx = testdbseed.OwnerCaller(t, ctx, sqlDB2)
	st2 := store.NewSQL(sqlDB2)
	reopenedSess, err := st2.Get(ctx, sess.ID)
	testutil.FailErr(t, "get reopened session", err)
	if reopenedSess.Mode != api.SessionModeAgent {
		t.Fatalf("reopened session mode = %s, want agent", reopenedSess.Mode)
	}

	var requests [][]api.Message
	failNoticeAppend := true
	appendErr := errors.New("simulated transient append failure")
	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			ResolveEffectiveMode: st2.ResolveEffectiveMode,
			Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return append([]api.Message(nil), history...), nil
			},
			ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
		},
		Model: ModelDeps{
			LLM: &askModeTestLLMClient{requests: &requests},
			CompactAfterContextOverflow: func(_ context.Context, _ *api.Session, history []api.Message, _ int) ([]api.Message, error) {
				var out []api.Message
				for _, m := range history {
					if m.HostSignalID != anchor.SessionAgentEnabled.String() {
						out = append(out, m)
					}
				}
				return out, nil
			},
		},
		Closeout: CloseoutDeps{
			CloseoutTrackingDeps: CloseoutTrackingDeps{
				RenderHostKick: func(_ context.Context, kickID string, _ map[string]any) (string, error) {
					return "[host:" + kickID + "]\nAgent mode is now active.", nil
				},
			},
		},
		Projection: ProjectionDeps{
			AppendMessages: func(ctx context.Context, sessionID string, msgs ...api.Message) error {
				for _, m := range msgs {
					if m.HostSignalID == anchor.SessionAgentEnabled.String() && failNoticeAppend {
						return appendErr
					}
				}
				return st2.AppendMessages(ctx, sessionID, msgs...)
			},
		},
	})

	// 3. First attempt after reopen fails when appending session.mode.agent_enabled.
	historyBefore, err := st2.GetMessages(ctx, reopenedSess.ID)
	testutil.FailErr(t, "load history before failed attempt", err)
	_, err = loop.Run(ctx, PromptRunInput{
		SessionID:  reopenedSess.ID,
		Session:    reopenedSess,
		History:    historyBefore,
		ProfileID:  "coordinator",
		UserPrompt: "Run in Agent mode",
	})
	if !errors.Is(err, appendErr) {
		t.Fatalf("expected appendErr on failed notice append, got %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("model requests on failed append = %d, want 0", len(requests))
	}

	// 4. Retry succeeds and delivers session.mode.agent_enabled once in the first eligible request.
	failNoticeAppend = false
	historyRetry, err := st2.GetMessages(ctx, reopenedSess.ID)
	testutil.FailErr(t, "load history for retry", err)
	_, err = loop.Run(ctx, PromptRunInput{
		SessionID:  reopenedSess.ID,
		Session:    reopenedSess,
		History:    historyRetry,
		ProfileID:  "coordinator",
		UserPrompt: "Run in Agent mode",
	})
	testutil.FailErr(t, "retry turn after failed append", err)
	if len(requests) != 1 {
		t.Fatalf("model requests after retry = %d, want 1", len(requests))
	}
	countSignal := func(msgs []api.Message) int {
		n := 0
		for _, m := range msgs {
			if m.HostSignalID == anchor.SessionAgentEnabled.String() {
				n++
			}
		}
		return n
	}
	if got := countSignal(requests[0]); got != 1 {
		t.Fatalf("first eligible request after reopen/retry notices = %d, want 1", got)
	}

	// 5. Subsequent turn on the upgraded session does not re-emit the notice.
	historyNext, err := st2.GetMessages(ctx, reopenedSess.ID)
	testutil.FailErr(t, "load history for later turn", err)
	if got := countSignal(historyNext); got != 1 {
		t.Fatalf("persisted transcript notices = %d, want 1", got)
	}
	_, err = loop.Run(ctx, PromptRunInput{
		SessionID:  reopenedSess.ID,
		Session:    reopenedSess,
		History:    historyNext,
		ProfileID:  "coordinator",
		UserPrompt: "Second Agent turn",
	})
	testutil.FailErr(t, "run second Agent turn", err)
	historyAfterSecond, err := st2.GetMessages(ctx, reopenedSess.ID)
	testutil.FailErr(t, "load history after second Agent turn", err)
	if got := countSignal(historyAfterSecond); got != 1 {
		t.Fatalf("persisted transcript notices after second Agent turn = %d, want 1", got)
	}

	// 6. After context compaction removes the delivered signal from the model window,
	// another eligible round (including after another reopen) must not duplicate it.
	turnState := &promptLoopTurnState{history: append([]api.Message(nil), historyAfterSecond...)}
	compactedWindow := loop.Model.recoverFromContextOverflow(
		ctx, reopenedSess, reopenedSess.ID, turnState.history, turnState, "", "test-provider", "test-model", 1000,
		&failure.ProviderContextTooSmallError{MaxContext: 100000},
	)
	if got := countSignal(compactedWindow); got != 0 || countSignal(turnState.history) != 0 {
		t.Fatalf("compacted window still contains notice: %d", got)
	}
	authAfterDeliver, err := st2.ResolveEffectiveMode(ctx, reopenedSess.ID)
	testutil.FailErr(t, "resolve authority after notice delivery", err)
	if !authAfterDeliver.UpgradedFromAsk || !authAfterDeliver.AgentNoticeDelivered {
		t.Fatalf("authority after notice delivery = %+v, want UpgradedFromAsk=true AgentNoticeDelivered=true", authAfterDeliver)
	}
	beforeCompactedReqs := len(requests)
	_, err = loop.Run(ctx, PromptRunInput{
		SessionID:  reopenedSess.ID,
		Session:    reopenedSess,
		History:    compactedWindow,
		ProfileID:  "coordinator",
		UserPrompt: "Third Agent turn with compacted window",
	})
	testutil.FailErr(t, "run third Agent turn with compacted window", err)
	if len(requests) != beforeCompactedReqs+1 {
		t.Fatalf("model requests after compacted turn = %d, want %d", len(requests), beforeCompactedReqs+1)
	}
	if got := countSignal(requests[len(requests)-1]); got != 0 {
		t.Fatalf("compacted-window request re-emitted notice %d times, want 0", got)
	}
	historyAfterCompacted, err := st2.GetMessages(ctx, reopenedSess.ID)
	testutil.FailErr(t, "load history after compacted turn", err)
	if got := countSignal(historyAfterCompacted); got != 1 {
		t.Fatalf("persisted transcript notices after compacted turn = %d, want 1", got)
	}

	// 7. Ordinary Agent session never receives session.mode.agent_enabled.
	reopenedOrdinary, err := st2.Get(ctx, ordinary.ID)
	testutil.FailErr(t, "get reopened ordinary session", err)
	_, err = loop.Run(ctx, PromptRunInput{
		SessionID:  reopenedOrdinary.ID,
		Session:    reopenedOrdinary,
		History:    nil,
		ProfileID:  "coordinator",
		UserPrompt: "Ordinary Agent turn",
	})
	testutil.FailErr(t, "run ordinary Agent turn", err)
	ordinaryMsgs, err := st2.GetMessages(ctx, reopenedOrdinary.ID)
	testutil.FailErr(t, "load ordinary session messages", err)
	if got := countSignal(ordinaryMsgs); got != 0 {
		t.Fatalf("ordinary Agent session notices = %d, want 0", got)
	}
}
