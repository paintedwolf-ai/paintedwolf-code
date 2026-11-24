package sessions

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTerminalWaitDeliveryAcrossAdmissionPublicationAndRecovery(t *testing.T) {
	h := newTerminalWaitHarness(t)

	var phaseMu sync.Mutex
	var phases []awaitstore.PhaseRecord
	unsub := awaitstore.RegisterPhaseObserver(func(rec awaitstore.PhaseRecord) {
		phaseMu.Lock()
		phases = append(phases, rec)
		phaseMu.Unlock()
	})
	defer unsub()

	// 1. Published terminal report before wait: 0 park, 0 lease, 0 timer ticks, 1 transcript observation.
	handle1, comp1 := h.startAndSettleJob(h.sess.ID, "stdout-stream-alpha\n", "stderr-stream-beta\n")
	h.procCtrl.HandleCommandCompletion(t.Context(), comp1)
	dispatchedAt := h.now.Add(-15 * time.Millisecond)
	msgs := h.runAgentToolTurn(awaitstore.WithDispatchTime(t.Context(), dispatchedAt), api.ToolCall{
		ID:   "wait-published-1",
		Name: "wait",
		Args: map[string]any{"until_complete": true, "conditions": []any{map[string]any{"kind": "process_done", "handles": []any{handle1}}}},
	})
	if h.parkCalls.Load() != 0 || h.timerTicks.Load() != 0 {
		t.Fatalf("published wait parked=%d ticks=%d", h.parkCalls.Load(), h.timerTicks.Load())
	}
	activeLeases, err := h.waitStore.Active(t.Context())
	testutil.FailErr(t, "active leases after ready wait", err)
	if len(activeLeases) != 0 {
		t.Fatalf("expected 0 leases, got %+v", activeLeases)
	}
	var toolObservations int
	for _, m := range msgs {
		if m.ToolResult != nil && m.ToolResult.ToolCallID == "wait-published-1" {
			toolObservations++
			if !strings.Contains(m.Content, "stdout-stream-alpha") || !strings.Contains(m.Content, "stderr-stream-beta") {
				t.Fatalf("wait result missing original streams: %s", m.Content)
			}
		}
	}
	if toolObservations != 1 {
		t.Fatalf("expected 1 transcript observation, got %d", toolObservations)
	}
	// Duplicate terminal signal cannot redeliver or emit unsolicited guidance.
	h.unsolicited.Store(0)
	h.procCtrl.HandleCommandCompletion(t.Context(), comp1)
	if h.unsolicited.Load() != 0 {
		t.Fatalf("duplicate completion emitted %d unsolicited signals", h.unsolicited.Load())
	}

	// 2. Admission hold race: publish report while held, then release -> 0 lease, 1 result.
	handle2, comp2 := h.startAndSettleJob(h.sess.ID, "race-stream-ok\n", "")
	entered := make(chan struct{})
	release := make(chan struct{})
	h.credGate.mu.Lock()
	h.credGate.entered, h.credGate.release = entered, release
	h.credGate.mu.Unlock()

	raceDone := make(chan []api.Message, 1)
	go func() {
		raceDone <- h.runAgentToolTurn(t.Context(), api.ToolCall{
			ID:   "wait-held-admission-2",
			Name: "wait",
			Args: map[string]any{"until_complete": true, "conditions": []any{map[string]any{"kind": "process_done", "handles": []any{handle2}}}},
		})
	}()
	<-entered
	h.procCtrl.HandleCommandCompletion(t.Context(), comp2)
	h.credGate.mu.Lock()
	h.credGate.entered, h.credGate.release = nil, nil
	h.credGate.mu.Unlock()
	close(release)
	raceMsgs := <-raceDone
	if h.parkCalls.Load() != 0 {
		t.Fatalf("admission-held wait parked %d times, want 0", h.parkCalls.Load())
	}
	foundRace := false
	for _, m := range raceMsgs {
		if m.ToolResult != nil && m.ToolResult.ToolCallID == "wait-held-admission-2" && strings.Contains(m.Content, "race-stream-ok") {
			foundRace = true
		}
	}
	if !foundRace {
		t.Fatal("admission-held wait did not deliver published terminal result")
	}

	// 3. Running job: arms 1 lease, wakes on report publication with 0 timer ticks.
	jobRelease := make(chan struct{})
	runningHandle, err := h.bg.StartProgram(t.Context(), bgprocess.ProgramJobSpec{
		SessionID: h.sess.ID,
		Run: func(_ context.Context, out, _ io.Writer) (bgprocess.ProgramJobResult, error) {
			<-jobRelease
			_, _ = io.WriteString(out, "live-wake-output\n")
			return bgprocess.ProgramJobResult{ExitCode: 0}, nil
		},
	})
	testutil.FailErr(t, "start running job", err)
	h.timerTicks.Store(0)
	_ = h.runAgentToolTurn(t.Context(), api.ToolCall{
		ID:   "wait-running-3",
		Name: "wait",
		Args: map[string]any{"until_complete": true, "conditions": []any{map[string]any{"kind": "process_done", "handles": []any{runningHandle}}}},
	})
	activeLeases, err = h.waitStore.Active(t.Context())
	testutil.FailErr(t, "active lease for running job", err)
	if len(activeLeases) != 1 {
		t.Fatalf("expected 1 armed lease, got %d", len(activeLeases))
	}
	close(jobRelease)
	_, _, err = h.bg.Lifecycle.Wait(t.Context(), h.sess.ID, runningHandle)
	testutil.FailErr(t, "wait running job exit", err)
	h.procCtrl.HandleCommandCompletion(t.Context(), bgprocess.Completion{
		SessionID: h.sess.ID, Handle: runningHandle, OriginTool: "command",
		TerminationReason: bgprocess.TerminationExited, FinishedAt: h.now,
		Tail: "live-wake-output", ExitObservation: &bgprocess.ExitObservation{},
	})
	resumedCond := <-h.resumeDone
	h.loop.Turns.WaitForAsyncTurns(t.Context())
	if !strings.Contains(resumedCond.Report, "live-wake-output") || resumedCond.Outcome != "satisfied" {
		t.Fatalf("event wake condition = %+v", resumedCond)
	}

	// 4. Termination before report publication: finished job with unpublished report parks instead of returning empty.
	handle4, comp4 := h.startAndSettleJob(h.sess.ID, "unpublished-then-published\n", "")
	_ = h.runAgentToolTurn(t.Context(), api.ToolCall{
		ID:   "wait-unpub-4",
		Name: "wait",
		Args: map[string]any{"until_complete": true, "conditions": []any{map[string]any{"kind": "process_done", "handles": []any{handle4}}}},
	})
	activeLeases, err = h.waitStore.Active(t.Context())
	testutil.FailErr(t, "active lease on unpublished exit", err)
	if len(activeLeases) != 1 {
		t.Fatalf("finished job with unpublished report did not arm lease: %+v", activeLeases)
	}
	h.procCtrl.HandleCommandCompletion(t.Context(), comp4)
	resumed4 := <-h.resumeDone
	h.loop.Turns.WaitForAsyncTurns(t.Context())
	if !strings.Contains(resumed4.Report, "unpublished-then-published") {
		t.Fatalf("resumed4 = %+v", resumed4)
	}

	// 5. Missing and wrong-session handles + 6. wait/output competition + 7. Restart recovery.
	otherHandle, _ := h.startAndSettleJob("other-session", "foreign-secret", "")
	for _, bad := range []string{"missing-handle", otherHandle} {
		badMsgs := h.runAgentToolTurn(t.Context(), api.ToolCall{
			ID:   "wait-bad-" + bad,
			Name: "wait",
			Args: map[string]any{"timeout_ms": 1000, "conditions": []any{map[string]any{"kind": "process_done", "handles": []any{bad}}}},
		})
		sawUnavail := false
		for _, m := range badMsgs {
			if m.ToolResult != nil && m.ToolResult.ToolCallID == "wait-bad-"+bad && strings.Contains(m.Content, `"outcome":"unavailable"`) {
				sawUnavail = true
			}
		}
		if !sawUnavail {
			t.Fatalf("handle %q was not reported unavailable", bad)
		}
	}

	// Competition with command_output + restart recovery.
	handle6, comp6 := h.startAndSettleJob(h.sess.ID, "shared-stream-6\n", "")
	_ = h.runAgentToolTurn(t.Context(), api.ToolCall{
		ID: "out-6", Name: "command_output", Args: map[string]any{"handle": handle6},
	})
	h.procCtrl.HandleCommandCompletion(t.Context(), comp6)
	compMsgs := h.runAgentToolTurn(t.Context(), api.ToolCall{
		ID:   "wait-comp-6",
		Name: "wait",
		Args: map[string]any{"until_complete": true, "conditions": []any{map[string]any{"kind": "process_done", "handles": []any{handle6}}}},
	})
	sawComp := false
	for _, m := range compMsgs {
		if m.ToolResult != nil && m.ToolResult.ToolCallID == "wait-comp-6" && strings.Contains(m.Content, "shared-stream-6") {
			sawComp = true
		}
	}
	if !sawComp {
		t.Fatal("command_output suppressed subsequent explicit wait report")
	}

	// Restart recovery: arm lease on unpublished handle, publish while offline, recover into fresh LoopEngine.
	handle7, comp7 := h.startAndSettleJob(h.sess.ID, "recovered-after-restart\n", "")
	_, err = h.waitStore.Arm(t.Context(), awaitstore.Lease{
		SessionID: h.sess.ID, ProjectID: testdbseed.DefaultProjectID,
		Conditions: []awaitstore.Condition{{Kind: "process_done", Handles: []string{handle7}}},
	})
	testutil.FailErr(t, "arm restart lease", err)
	h.procCtrl.SetLoop(nil, nil)
	h.procCtrl.HandleCommandCompletion(t.Context(), comp7)
	restarted := h.newLoopEngine()
	restarted.Subscriptions.SetWaitStore(h.waitStore)
	t.Cleanup(func() { restarted.ForgetSession(context.Background(), h.sess.ID) })
	testutil.FailErr(t, "recover wait leases", loopwake.RecoverWaitLeases(t.Context(), restarted.Subscriptions, h.waitStore))
	recovered := <-h.resumeDone
	restarted.Turns.WaitForAsyncTurns(t.Context())
	if !strings.Contains(recovered.Report, "recovered-after-restart") {
		t.Fatalf("recovered report = %+v", recovered)
	}

	// Verify phase records contain zero command bodies or stream outputs.
	phaseMu.Lock()
	rawPhases, err := json.Marshal(phases)
	phaseMu.Unlock()
	testutil.FailErr(t, "marshal phases", err)
	for _, secret := range []string{"stdout-stream-alpha", "stderr-stream-beta", "race-stream-ok", "live-wake-output"} {
		if strings.Contains(string(rawPhases), secret) {
			t.Fatalf("phase diagnostics leaked stream payload %q", secret)
		}
	}
}
