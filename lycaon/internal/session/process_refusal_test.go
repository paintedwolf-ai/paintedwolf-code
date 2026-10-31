package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissions"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func refusalObservation() confine.Observation {
	return confine.Observation{Applied: true, Running: true, Refusals: confine.SandboxRefusals{
		Witness: confine.WitnessIncomplete, Omitted: 4,
		Refusals: []confine.SandboxRefusal{{Operation: "network-bind", Target: "/tmp/user.sock", Process: "limactl", Count: 2, Recovery: confine.RecoverHostExecution},
			{Operation: "file-write-create", Target: "/outside/file", Process: "touch", Count: 1, Recovery: confine.RecoverWriteRoot, Grant: "/outside"}},
	}}
}

func assertRefusalDigest(t *testing.T, digest string) {
	t.Helper()
	for _, want := range []string{
		"handle=command-1", "tool=command", "command=vm start", "sandbox_refusals:",
		"network-bind /tmp/user.sock (limactl) count=2 recovery=host_execution",
		"file-write-create /outside/file (touch) count=1 recovery=write_root grant=/outside",
		"sandbox_refusals_omitted=4", "sandbox_refusal_witness=incomplete",
	} {
		if !strings.Contains(digest, want) {
			t.Errorf("digest missing %q: %s", want, digest)
		}
	}
}

func TestHandleCommandRefusalWakesWithDigest(t *testing.T) {
	for _, offered := range [][]string{nil, {}, {"command_output"}, {"command_stop"}, {"command_output", "command_stop"}} {
		t.Run(strings.Join(offered, "+"), func(t *testing.T) {
			testHandleCommandRefusalWakesWithDigest(t, offered)
		})
	}
}

func testHandleCommandRefusalWakesWithDigest(t *testing.T, offered []string) {
	memory := store.NewMemory()
	mgr := NewHost(memory, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	mgr.Processes.ToolPolicy = newPostToolGuidanceManager(t).ToolPolicy
	sess, err := memory.Create(t.Context(), api.CreateSessionRequest{}, "")
	testutil.FailErr(t, "create waiting session", err)
	loop := mgr.Coordinator.Runtime.CoordinatorLoop()
	reports := make(chan anchor.Envelope, 1)
	deps := mgr.Coordinator.Loop.Build()
	deps.HostWakeActionable = func(context.Context, loopwake.HostWakeActionableInput) bool { return true }
	deps.QueueInform = func(_ context.Context, _ string, inform anchor.ID, env anchor.Envelope) {
		if inform == anchor.ProcessRefused {
			reports <- env
		}
	}
	loop.SetDeps(deps)
	loop.Waits.EnterSleep(t.Context(), sess.ID, time.Time{}, "waiting for command",
		[]loopwake.WaitTrigger{loopwake.WaitTriggerProcessDone}, []string{"command-1"}, loopwake.SleepMoverHost)
	ctx := t.Context()
	if offered != nil {
		ctx = tools.WithRecoveryTools(ctx, offered)
	}
	mgr.Processes.HandleCommandRefusal(ctx, bgprocess.RefusalNotice{
		SessionID: sess.ID, Handle: "command-1", OriginTool: "command", Mode: bgprocess.JobModeBackground,
		StartedAt: time.Now(), Unshown: 2, Stages: []hostcmd.StageResult{{Command: "vm start"}}, Observation: refusalObservation(),
	})
	if loop.Waits.IsSleeping(sess.ID) {
		t.Fatal("refusal left the matching process wait asleep")
	}
	select {
	case env := <-reports:
		assertRefusalDigest(t, env.CommandRefusalDigest)
		engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
		text, err := engine.RenderKick(t.Context(), "coordinator-process-refused", map[string]any{"command_refusal": env.CommandRefusalDigest})
		testutil.FailErr(t, "render process refusal wake", err)
		for _, control := range []string{"command_output", "command_stop"} {
			if strings.Contains(text, "`"+control) != tools.ToolOffered(ctx, control) {
				t.Errorf("wake recommends unavailable control %s: %s", control, text)
			}
		}
		for _, required := range []string{"Do not reissue while the job is running", "A stop request alone does not", "Preserve completed effects"} {
			if !strings.Contains(text, required) {
				t.Errorf("wake lost %q: %s", required, text)
			}
		}
		if strings.Contains(text, "and rerun it") {
			t.Errorf("wake bypassed observed exit: %s", text)
		}
		if !strings.Contains(env.CommandRefusalDigest, "state=running") || !strings.Contains(env.CommandRefusalDigest, "unshown_refusals=2") {
			t.Fatalf("refusal lost running state: %s", env.CommandRefusalDigest)
		}
	case <-t.Context().Done():
		t.Fatal("refusal wake omitted the report")
	}
	loop.Turns.WaitForAsyncTurns(t.Context())
}

func TestCommandCompletionDigestIncludesRefusalsAndTimeout(t *testing.T) {
	completion := bgprocess.Completion{Handle: "command-1", OriginTool: "command", Mode: bgprocess.JobModeBackground,
		TerminationReason: bgprocess.TerminationTimedOut, ExitCode: -1,
		Stages: []hostcmd.StageResult{{Command: "vm start"}}, Observation: refusalObservation(), Tail: "still waiting"}
	digest := processcontrol.CommandCompletionDigest(completion)
	assertRefusalDigest(t, digest)
	for _, want := range []string{"termination=timed_out", "exit_code=-1", "tail:\nstill waiting"} {
		if !strings.Contains(digest, want) {
			t.Errorf("completion missing %q: %s", want, digest)
		}
	}
}

func TestWaitWinnerTextRetainsTheProcessReport(t *testing.T) {
	for _, tc := range []struct{ outcome, report string }{
		{"satisfied", "handle=command-1 termination=timed_out exit_code=-1"},
		{"refused", "handle=command-1 state=running\nsandbox_refusals:\n- network-bind /tmp/user.sock"},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			text := submissions.WaitWinnerText(awaitstore.Condition{Kind: "process_done", Outcome: tc.outcome, Report: tc.report})
			if !strings.Contains(text, tc.report) {
				t.Fatalf("resumed prompt lost the process report: %s", text)
			}
		})
	}
}

func TestMixedPathRecoveryKeepsTerminalPathsOutsideRequestedGrants(t *testing.T) {
	mgr := newPostToolGuidanceManager(t)
	for _, access := range []struct {
		operation, code           string
		grantLayer, terminalLayer confine.FloorLayer
		recovery                  confine.RefusalRecovery
	}{
		{"file-read-data", "SANDBOX_TRY_READ_PATH", confine.FloorReadKeyMaterial, confine.FloorReadControlPlane, confine.RecoverReadPath},
		{"file-write-data", "SANDBOX_TRY_WRITE_ROOT", confine.FloorOutsideWriteRoots, confine.FloorControlPlane, confine.RecoverWriteRoot},
	} {
		for _, running := range []bool{false, true} {
			for offered := 0; offered < 4; offered++ {
				names := []string{}
				if offered&1 != 0 {
					names = append(names, "command_output")
				}
				if offered&2 != 0 {
					names = append(names, "command_stop")
				}
				obs := confine.Observation{Applied: true, Running: running, Refusals: confine.SandboxRefusals{Witness: confine.WitnessKernel, Refusals: []confine.SandboxRefusal{
					{Operation: access.operation, Target: "/requestable/file", Grant: "/requestable/file", Layer: access.grantLayer, Recovery: access.recovery},
					{Operation: access.operation, Target: "/host/control", Layer: access.terminalLayer, Recovery: confine.RecoverNone},
				}}}
				if !running {
					obs.FailedStages = []string{"fixture"}
				}
				ctx := tools.WithRecoveryTools(t.Context(), names)
				text, facts := mgr.ToolPolicy.AfterTool(ctx, &api.Session{ID: t.Name()}, "command", nil, "{}", 1, guidance.ToolResultFacts{Confine: obs})
				if !facts.HasCode(access.code) || !facts.HasCode("SANDBOX_TERMINAL_PATH_REFUSED") {
					t.Fatalf("mixed refusals lost a remedy: %s", text)
				}
				for _, want := range []string{"/requestable/file", "/host/control", "other refused paths may remain unavailable", "no approval opens them"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %q: %s", want, text)
					}
				}
				for i, control := range []string{"command_output", "command_stop"} {
					if strings.Contains(text, "`"+control+"`") != (running && offered&(1<<i) != 0) {
						t.Errorf("control %s running=%v offered=%v: %s", control, running, names, text)
					}
				}
				if running && !strings.Contains(text, "Do not reissue while the job is running") {
					t.Errorf("lost settlement requirement: %s", text)
				}
			}
		}
	}
}

func TestCommandCompletionWakesWorkerProcessWait(t *testing.T) {
	memory := store.NewMemory()
	mgr := NewHost(memory, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
	sess, err := memory.Create(t.Context(), api.CreateSessionRequest{}, "")
	testutil.FailErr(t, "create waiting worker session", err)
	testutil.FailErr(t, "mark worker session", memory.UpdateSession(t.Context(), sess.ID, func(s *api.Session) {
		s.ParentSessionID = "parent"
	}))
	loop := mgr.Coordinator.Runtime.CoordinatorLoop()
	loop.Waits.EnterSleep(t.Context(), sess.ID, time.Time{}, "waiting for command completion",
		[]loopwake.WaitTrigger{loopwake.WaitTriggerProcessDone}, []string{"command-1"}, loopwake.SleepMoverHost)
	mgr.Processes.HandleCommandCompletion(t.Context(), bgprocess.Completion{SessionID: sess.ID, Handle: "other"})
	if !loop.Waits.IsSleeping(sess.ID) {
		t.Fatal("unrelated completion broke worker wait")
	}
	mgr.Processes.HandleCommandCompletion(t.Context(), bgprocess.Completion{SessionID: sess.ID, Handle: "command-1"})
	if loop.Waits.IsSleeping(sess.ID) {
		t.Fatal("worker wait ignored command completion")
	}
}

func TestAutoNameProjectIdempotentOnReplay(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	project.SetDefaultOpenPolicy(project.TestOpenPolicy())

	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Draft: true})
	testutil.FailErr(t, "create project", err)

	store := store.NewMemory()
	mock := llm.NewMockProvider(&llm.MockConfig{Responses: []llm.MockResponseEntry{
		{Pattern: ".*", Text: "ok"},
	}})
	mgr := NewHost(store, Models{Client: mock, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, tools.NewStubRegistry())
	mgr.SetProjectRegistry(reg)
	hub := events.NewMemoryHub()
	mgr.SetEventPublisher(&events.Publisher{Hub: hub, Lookup: project.ScopeLookup{Registry: reg}})

	sess, err := mgr.Chats.CreateForProject(ctx, p.ID, api.SessionPostureBuild)
	testutil.FailErr(t, "create session", err)

	userText := "Build a CLI todo tracker with SQLite persistence"

	eventsCh, unsubscribe, err := hub.Subscribe(ctx, events.Subscription{Project: p.ID, Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe", err)
	defer unsubscribe()

	mgr.Chats.Naming.ProjectFromPrompt(ctx, sess, userText)
	mgr.Chats.Naming.ProjectFromPrompt(ctx, sess, userText)
	hub.FlushDebounced()

	got, err := reg.Get(ctx, p.ID)
	testutil.FailErr(t, "get project", err)
	want := project.NameProject(ctx, nil, userText)
	if got.Name != want {
		t.Fatalf("name = %q want %q", got.Name, want)
	}

	updated := 0
	for {
		select {
		case envelope := <-eventsCh:
			if envelope.Topic != api.EventTopicProject {
				continue
			}
			var ev api.ProjectEvent
			if err := json.Unmarshal(envelope.Data, &ev); err != nil {
				testutil.FailErr(t, "decode project event", err)
			}
			if ev.Action == api.ProjectEventUpdated {
				updated++
			}
		default:
			if updated != 1 {
				t.Fatalf("project.updated count = %d want 1", updated)
			}
			return
		}
	}
}

func TestInterruptedToolProjectionRetainsIdentityAcrossRecoveryPaths(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		name := "startup"
		if scoped {
			name = "session stop"
		}
		t.Run(name, func(t *testing.T) {
			database := testdbfixture.Open(t, "interrupted-identity.db")
			testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())
			st := store.NewSQL(database)
			manager := NewHost(st, Models{Client: nil, Provider: nil, Limits: settings.DefaultSessionLimits(), Cost: nil}, nil)
			recorder := invocation.NewSQLRecorder(database)
			manager.SetInvocationRecorder(recorder)
			sess, err := st.Create(t.Context(), api.CreateSessionRequest{}, testdbseed.DefaultProjectID)
			testutil.FailErr(t, "create interrupted session", err)
			for _, tool := range []string{"read", "command", "http_request"} {
				seedInterruptedTool(t, st, recorder, sess.ID, tool)
			}
			_, err = recorder.InterruptRunning(t.Context())
			testutil.FailErr(t, "interrupt tool owners", err)
			for range 2 {
				if scoped {
					err = manager.Stops.Recovery.RecoverInterruptedToolResultPagesForSession(t.Context(), recorder, sess.ID)
				} else {
					err = manager.Stops.Recovery.RecoverInterruptedToolResults(t.Context())
				}
				testutil.FailErr(t, "recover tool projections", err)
			}
			messages, err := st.GetMessages(t.Context(), sess.ID)
			testutil.FailErr(t, "read recovered transcript", err)
			assertRecoveredToolIdentities(t, messages)
		})
	}
}

func seedInterruptedTool(t *testing.T, st *store.SQL, recorder *invocation.SQLRecorder, sessionID, tool string) {
	t.Helper()
	contract, ok := toolcontract.Lookup(tool)
	if !ok {
		t.Fatalf("missing tool contract %s", tool)
	}
	testutil.FailErr(t, "append tool request", st.AppendMessages(t.Context(), sessionID, api.Message{
		ID: "assistant-" + tool, Role: api.MessageRoleAssistant,
		ToolCalls: []api.ToolCall{{ID: "call-" + tool, Name: tool}},
	}))
	_, err := recorder.Begin(t.Context(), invocation.Start{
		ProjectID: testdbseed.DefaultProjectID, SessionID: sessionID,
		AssistantMessageID: "assistant-" + tool, ToolCallID: "call-" + tool,
		ToolName: tool, Contract: contract, Args: map[string]any{},
	})
	testutil.FailErr(t, "begin tool receipt", err)
}

func assertRecoveredToolIdentities(t *testing.T, messages []api.Message) {
	t.Helper()
	counts := make(map[string]int)
	for _, message := range messages {
		result := message.ToolResult
		if result == nil {
			continue
		}
		counts[result.Tool]++
		if result.AssistantMessageID != "assistant-"+result.Tool || result.ToolCallID != "call-"+result.Tool {
			t.Errorf("recovered result cannot join its call: %+v", result)
		}
		if result.Invocation == nil || result.Invocation.Status != api.InvocationStatusInterrupted {
			t.Errorf("recovered result lacks interrupted owner evidence: %+v", result)
		}
	}
	for _, tool := range []string{"read", "command", "http_request"} {
		if counts[tool] != 1 {
			t.Errorf("%s result count = %d, want one after replay", tool, counts[tool])
		}
	}
}
