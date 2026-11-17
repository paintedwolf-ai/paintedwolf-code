//go:build !windows

package command_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestConfinedForegroundPromotionSurvivesTurnCancelAndRetainsOutput(t *testing.T) {
	tool, reg := newCommandTool(t)
	ctx := commandToolContext(t.TempDir(), "promoted-live", "")
	captured := &tools.ToolInvocationOut{}
	ctx.Effects.Out = captured

	turnCtx, cancelTurn := context.WithCancel(t.Context())
	defer cancelTurn()

	args := map[string]any{
		"command": "printf 'phase-1\\n'; sleep 0.3; printf 'phase-2\\n'; sleep 10",
		"wait_ms": float64(50),
	}
	foreground := command.ConfinedForeground{Background: reg, Runner: tool.Runner, Boundary: tool.Boundary, ToolName: "command"}
	_, outcome, err := foreground.Run(turnCtx, args, ctx)
	testutil.FailErr(t, "run confined foreground command", err)
	if outcome.Finished {
		t.Fatal("foreground command ended before promotion")
	}
	raw, err := command.EncodeCommandRunning(ctx, outcome, command.WaitBudget(args))
	testutil.FailErr(t, "render promoted command", err)
	if captured.Process == nil || !captured.Process.Running || captured.Process.Handle == "" {
		t.Fatalf("expected promoted live process handle, got %+v (raw=%s)", captured.Process, raw)
	}
	handle := captured.Process.Handle
	t.Cleanup(func() {
		_ = reg.Lifecycle.DisposeSession(context.Background(), ctx.Identity.SessionID, bgprocess.TerminationChatDeleted)
	})

	// Turn stop releases awaited processes while promoted work retains its lifetime.
	cancelTurn()
	testutil.FailErr(t, "dispose awaited on turn stop", reg.Lifecycle.DisposeAwaitedSession(t.Context(), ctx.Identity.SessionID, bgprocess.TerminationStoppedByUser))

	if !reg.HasRunning(ctx.Identity.SessionID) {
		t.Fatal("promoted command stopped when turn context was cancelled and awaited processes disposed")
	}
	active := reg.ActiveJobs(ctx.Identity.SessionID)
	if len(active) != 1 || active[0].Handle != handle {
		t.Fatalf("active jobs after turn stop = %+v, want [%s]", active, handle)
	}

	outputTool := &command.CommandOutputTool{Registry: reg}
	deadline := time.Now().Add(5 * time.Second)
	for {
		outRaw, err := outputTool.Run(t.Context(), map[string]any{"handle": handle}, ctx)
		testutil.FailErr(t, "read promoted command output", err)
		if strings.Contains(outRaw, "phase-1") && strings.Contains(outRaw, "phase-2") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("promoted command did not deliver phase-2 output after turn cancel; last=%s", outRaw)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
