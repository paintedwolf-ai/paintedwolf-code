package session

import (
	"context"
	"strings"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
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
	memory := store.NewMemory()
	mgr := NewManager(memory, nil, nil, settings.DefaultSessionLimits())
	sess, err := memory.Create(t.Context(), api.CreateSessionRequest{}, "")
	testutil.FailErr(t, "create waiting session", err)
	mgr.SetCoordinatorRuntime(coordinator.NewRuntime(coordinator.RuntimeDeps{}))
	loop := mgr.ensureCoordinatorRuntime().CoordinatorLoop()
	reports := make(chan anchor.Envelope, 1)
	deps := mgr.buildLoopWakeDeps()
	deps.HostWakeActionable = func(context.Context, loopwake.HostWakeActionableInput) bool { return true }
	deps.QueueInform = func(_ context.Context, _ string, inform anchor.ID, env anchor.Envelope) {
		if inform == anchor.ProcessRefused {
			reports <- env
		}
	}
	loop.SetDeps(deps)
	loop.EnterSleep(t.Context(), sess.ID, time.Time{}, "waiting for command",
		[]loopwake.WaitTrigger{loopwake.WaitTriggerProcessDone}, []string{"command-1"}, loopwake.SleepMoverHost)
	mgr.HandleCommandRefusal(t.Context(), bgprocess.RefusalNotice{
		SessionID: sess.ID, Handle: "command-1", OriginTool: "command", Mode: bgprocess.JobModeBackground,
		StartedAt: time.Now(), Unshown: 2, Stages: []hostcmd.StageResult{{Command: "vm start"}}, Observation: refusalObservation(),
	})
	if loop.IsSleeping(sess.ID) {
		t.Fatal("refusal left the matching process wait asleep")
	}
	select {
	case env := <-reports:
		assertRefusalDigest(t, env.CommandRefusalDigest)
		if !strings.Contains(env.CommandRefusalDigest, "state=running") || !strings.Contains(env.CommandRefusalDigest, "unshown_refusals=2") {
			t.Fatalf("refusal lost running state: %s", env.CommandRefusalDigest)
		}
	case <-time.After(time.Second):
		t.Fatal("refusal wake omitted the report")
	}
	loop.WaitForAsyncTurns(testutil.BoundedContext(t, time.Second))
}

func TestCommandCompletionDigestIncludesRefusalsAndTimeout(t *testing.T) {
	completion := bgprocess.Completion{Handle: "command-1", OriginTool: "command", Mode: bgprocess.JobModeBackground,
		TerminationReason: bgprocess.TerminationTimedOut, ExitCode: -1,
		Stages: []hostcmd.StageResult{{Command: "vm start"}}, Observation: refusalObservation(), Tail: "still waiting"}
	digest := commandCompletionDigest(completion)
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
			text := waitWinnerText(awaitstore.Condition{Kind: "process_done", Outcome: tc.outcome, Report: tc.report})
			if !strings.Contains(text, tc.report) {
				t.Fatalf("resumed prompt lost the process report: %s", text)
			}
		})
	}
}
