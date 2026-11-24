package sessions

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/promptresult"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/pkg/api"
)

type gatedCredentialFiles struct {
	mu      sync.Mutex
	entered chan struct{}
	release chan struct{}
}

func (*gatedCredentialFiles) Start(string) {}

func (g *gatedCredentialFiles) Await(ctx context.Context, _ tools.CredentialWait) error {
	g.mu.Lock()
	entered, release := g.entered, g.release
	g.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release:
		}
	}
	return nil
}
func (*gatedCredentialFiles) ForgetProject(context.Context, string) error               { return nil }
func (*gatedCredentialFiles) Delivered(context.Context, tools.CredentialFileRead) error { return nil }
func (*gatedCredentialFiles) Authored(context.Context, tools.AuthoredCredentialValues) error {
	return nil
}

type scriptedWaitLLM struct {
	mu    sync.Mutex
	turns []*modelcall.Completion
	seen  []modelcall.CompletionRequest
}

func (c *scriptedWaitLLM) Complete(_ context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, req)
	if len(c.turns) == 0 {
		return &modelcall.Completion{Content: "settled"}, nil
	}
	next := c.turns[0]
	c.turns = c.turns[1:]
	return next, nil
}

func (c *scriptedWaitLLM) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	comp, err := c.Complete(ctx, req)
	if err != nil {
		return nil, err
	}
	ch := make(chan modelcall.StreamChunk, 2)
	ch <- modelcall.StreamChunk{Content: comp.Content, ToolCalls: comp.ToolCalls, Done: true}
	close(ch)
	return ch, nil
}

type noopTurnPolicy struct{ metas []tools.ToolMeta }

func (p noopTurnPolicy) ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta {
	return p.metas
}
func (noopTurnPolicy) EvaluateInvoke(context.Context, *api.Session, string, map[string]any) error {
	return nil
}
func (*noopTurnPolicy) AfterTool(_ context.Context, _ *api.Session, _ string, _ map[string]any, out string, _ int, f guidance.ToolResultFacts) (string, guidance.ToolResultFacts) {
	return out, f
}

type terminalWaitHarness struct {
	t           *testing.T
	storage     *store.Repositories
	sess        *api.Session
	waitStore   *awaitstore.Store
	bg          *bgprocess.Registry
	procCtrl    *processcontrol.Service
	loop        *loopwake.LoopEngine
	executor    *toolexecution.Executor
	execReg     *tools.ExecutorRegistry
	credGate    *gatedCredentialFiles
	nowMu       sync.Mutex
	now         time.Time
	timerTicks  atomic.Int32
	parkCalls   atomic.Int32
	unsolicited atomic.Int32
	resumeDone  chan awaitstore.Condition
}

func (h *terminalWaitHarness) RecordSourceRunTerminal(context.Context, *api.Session, string, string, tools.SourceRunCapture, time.Time) {
}
func (h *terminalWaitHarness) Emit(_ context.Context, _ string, id anchor.ID, _ anchor.Envelope) {
	if id == anchor.ProcessFinished {
		h.unsolicited.Add(1)
	}
}

func newTerminalWaitHarness(t *testing.T) *terminalWaitHarness {
	t.Helper()
	guidance.SetGuidanceRenderer(promptstest.GuidanceRenderer(t))
	database := testdbfixture.Open(t, "sessions.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
	storage := store.NewSQL(database)
	sess, err := storage.Sessions.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	h := &terminalWaitHarness{
		t:          t,
		storage:    storage,
		sess:       sess,
		waitStore:  &awaitstore.Store{DB: database},
		credGate:   &gatedCredentialFiles{},
		now:        time.Date(2026, 11, 23, 17, 0, 39, 0, time.UTC),
		resumeDone: make(chan awaitstore.Condition, 8),
	}
	completions := make(chan bgprocess.Completion, 16)
	h.bg = bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{
		Complete: func(_ context.Context, c bgprocess.Completion) { completions <- c },
	})
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build matcher", err)
	h.bg.Output.SetCaptureProjector(captureprojection.New(matcher, nil))
	t.Cleanup(func() { _ = h.bg.Lifecycle.Close(context.Background()) })

	pol := &noopTurnPolicy{}
	h.procCtrl = processcontrol.New(storage.Sessions, h, pol, h)
	h.procCtrl.Background = h.bg

	h.loop = h.newLoopEngine()
	h.procCtrl.SetLoop(h.loop.Waits, h.loop.Nudges)
	t.Cleanup(func() { h.loop.ForgetSession(context.Background(), sess.ID) })

	reg := tools.NewDefaultRegistry()
	clock := awaitstore.Clock{
		NowFunc: func() time.Time {
			h.nowMu.Lock()
			defer h.nowMu.Unlock()
			return h.now
		},
		NewTimerFunc: func(time.Duration) (<-chan time.Time, func()) {
			h.timerTicks.Add(1)
			return make(chan time.Time), func() {}
		},
	}
	testutil.FailErr(t, "register wait", loopwake.RegisterWaitTool(reg, h.loop.Subscriptions, loopwake.WaitToolDeps{
		Store:       h.waitStore,
		Clock:       clock,
		WorkerKnown: func(context.Context, string, string) bool { return true },
	}))
	outTool := &command.CommandOutputTool{Registry: h.bg}
	testutil.FailErr(t, "register command_output", reg.Register("command_output", outTool.Run))

	h.executor = toolexecution.NewExecutor(nil, reg, "coordinator")
	h.execReg = tools.NewExecutorRegistry(h.executor, reg)
	return h
}

func (h *terminalWaitHarness) newLoopEngine() *loopwake.LoopEngine {
	loop := loopwake.NewLoopEngine()
	loop.SetDeps(loopwake.LoopDeps{
		GetSession: h.storage.Sessions.Get,
		Limits:     func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		PublishWaitLease: func(context.Context, string, loopwake.WaitLease) {
			h.parkCalls.Add(1)
		},
		RunWaitResume: func(ctx context.Context, sessionID string, delivery loopwake.WaitDelivery) (*promptresult.Result, error) {
			if err := delivery.Admitted(); err != nil {
				return nil, err
			}
			msg := api.Message{
				ID:        "resume-" + delivery.LeaseID,
				Role:      api.MessageRoleUser,
				Kind:      api.MessageKindHostLoopWake,
				Content:   loopwake.WaitEndedText(delivery.Condition),
				CreatedAt: time.Now().UTC(),
			}
			if err := h.storage.Transcript.AppendMessages(ctx, sessionID, msg); err != nil {
				return nil, err
			}
			loopwake.ObserveProcessWait(sessionID, delivery.Condition, h.procCtrl.Observe)
			awaitstore.EmitPhaseRecord(ctx, awaitstore.PhaseRecord{
				SessionID: sessionID, LeaseID: delivery.LeaseID,
				Handle: firstHandle(delivery.Condition.Handles), Tool: "wait",
				Phase: "transcript_result_commit", Outcome: delivery.Condition.Outcome,
			})
			h.resumeDone <- delivery.Condition
			return &promptresult.Result{}, nil
		},
		LoopProcessDeps: loopwake.LoopProcessDeps{
			ProcessRunning: h.procCtrl.HandlesRunning,
			ProcessState:   h.procCtrl.HandleState,
			ProcessSubject: h.procCtrl.HandleSubject,
			ProcessReport:  h.procCtrl.Report,
			ObserveProcess: h.procCtrl.Observe,
		},
	})
	return loop
}

func firstHandle(handles []string) string {
	if len(handles) > 0 {
		return handles[0]
	}
	return ""
}

func (h *terminalWaitHarness) runAgentToolTurn(ctx context.Context, call api.ToolCall) []api.Message {
	h.t.Helper()
	llmClient := &scriptedWaitLLM{turns: []*modelcall.Completion{
		{ToolCalls: []api.ToolCall{call}},
		{Content: "done"},
	}}
	pl := promptloop.NewPromptLoop(promptloop.PromptLoopDeps{
		Context: promptloop.ContextDeps{
			Limits:               func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
			Tools:                h.execReg,
			Policy:               &noopTurnPolicy{metas: h.execReg.List()},
			LoadedTools:          func(string) map[string]bool { return map[string]bool{"wait": true, "command_output": true} },
			ResolveEffectiveMode: h.storage.ResolveEffectiveMode,
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
			ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
		},
		Model: promptloop.ModelDeps{LLM: llmClient},
		Projection: promptloop.ProjectionDeps{
			AppendMessages: h.storage.Transcript.AppendMessages,
			UpdateMessage:  func(context.Context, string, string, api.Message) error { return nil },
		},
		Closeout: promptloop.CloseoutDeps{
			EvaluateCloseoutBlock: func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) { return nil, nil },
		},
	})
	_, err := pl.Run(ctx, promptloop.PromptRunInput{
		SessionID: h.sess.ID,
		Session:   h.sess,
		History:   []api.Message{{ID: "u-" + call.ID, Role: api.MessageRoleUser, Content: "check job"}},
		ProfileID: "coordinator",
		ToolCtx: tools.ToolContext{
			Identity: tools.InvocationIdentity{
				SessionID: h.sess.ID, RootSessionID: h.sess.ID,
				ProjectID: testdbseed.DefaultProjectID, Agent: "coordinator",
			},
			Effects: tools.InvocationEffects{CredentialFiles: h.credGate},
		},
	})
	testutil.FailErr(h.t, "run promptloop turn "+call.ID, err)
	msgs, err := h.storage.Transcript.GetMessages(h.t.Context(), h.sess.ID)
	testutil.FailErr(h.t, "read transcript", err)
	return msgs
}

func (h *terminalWaitHarness) startAndSettleJob(sessionID, stdoutText, stderrText string) (string, bgprocess.Completion) {
	h.t.Helper()
	obs := &bgprocess.ExitObservation{}
	handle, err := h.bg.StartProgram(h.t.Context(), bgprocess.ProgramJobSpec{
		SessionID: sessionID,
		ProjectID: testdbseed.DefaultProjectID,
		Run: func(_ context.Context, out, errOut io.Writer) (bgprocess.ProgramJobResult, error) {
			_, _ = io.WriteString(out, stdoutText)
			_, _ = io.WriteString(errOut, stderrText)
			return bgprocess.ProgramJobResult{ExitCode: 0}, nil
		},
	})
	testutil.FailErr(h.t, "start background program", err)
	_, _, err = h.bg.Lifecycle.Wait(h.t.Context(), sessionID, handle)
	testutil.FailErr(h.t, "wait background program", err)
	snap, err := h.bg.Snapshot(h.t.Context(), sessionID, handle, 4096)
	testutil.FailErr(h.t, "snapshot settled job", err)
	return handle, bgprocess.Completion{
		SessionID:         sessionID,
		Handle:            handle,
		OriginTool:        "command",
		ToolCallID:        "cmd-34",
		TerminationReason: bgprocess.TerminationExited,
		ExitCode:          0,
		FinishedAt:        h.now,
		Tail:              snap.Tail,
		ExitObservation:   obs,
	}
}
