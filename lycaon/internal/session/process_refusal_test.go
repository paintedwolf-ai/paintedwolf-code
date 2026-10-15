package session

import (
	"context"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/session/processcontrol"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/submissions"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
	"time"
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
	case <-time.After(time.Second):
		t.Fatal("refusal wake omitted the report")
	}
	loop.Turns.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
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
