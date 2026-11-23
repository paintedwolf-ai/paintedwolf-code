package command

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
)

type recordedTempReview struct {
	calls  int
	failed bool
	roots  []string
	ask    SandboxWriteRootAsk
}

func (r *recordedTempReview) ReviewSharedTemp(_ context.Context, ask SandboxWriteRootAsk, roots []string, failed bool) guidance.SharedTempSettlement {
	r.calls++
	r.roots, r.failed, r.ask = roots, failed, ask
	return guidance.SharedTempSettlement{Refused: roots, Allowed: roots}
}

func TestSharedTempReviewUsesSettledForegroundFacts(t *testing.T) {
	for _, tc := range []struct {
		name            string
		failed, running bool
		termination     string
		wantCalls       int
	}{
		{"failed", true, false, "exited", 1}, {"passed", false, false, "exited", 1}, {"running", true, true, "", 0}, {"interrupted", true, false, "stopped", 0}, {"unknown", false, false, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broker := &recordedTempReview{}
			runtime := ProgramRuntime{SharedTemp: broker}
			var tool tools.ToolContext
			tool.Host.SessionScratchDir = "/scratch"
			tool.Identity.SessionID, tool.Identity.ToolCallID = "worker", "call"
			tool.Effects.Out = &tools.ToolInvocationOut{}
			obs := confine.Observation{Applied: true, TerminationReason: tc.termination, Running: tc.running}
			if tc.failed {
				obs.FailedStages = []string{"build"}
			}
			for _, root := range []string{"/var/tmp", "/tmp", "/tmp"} {
				obs.Refusals.Refusals = append(obs.Refusals.Refusals, confine.SandboxRefusal{Operation: "file-write-create", Target: root + "/x", Layer: confine.FloorSharedTemp, Recovery: confine.RecoverWriteRoot, Grant: root})
			}
			obs.Refusals.Refusals = append(obs.Refusals.Refusals, confine.SandboxRefusal{Operation: "file-write-data", Target: "/outside/x", Layer: confine.FloorOutsideWriteRoots, Grant: "/outside"})
			tool.Effects.Out.Facts.Confine = obs
			runtime.reviewSharedTemp(t.Context(), tool, "command", "build", false)
			if broker.calls != tc.wantCalls {
				t.Fatalf("submissions=%d want %d", broker.calls, tc.wantCalls)
			}
			if broker.calls > 0 && (broker.failed != tc.failed || !slices.Equal(broker.roots, []string{"/tmp", "/var/tmp"}) || broker.ask.SessionID != "worker" || broker.ask.ToolCallID != "call" || !tool.Effects.Out.Facts.SharedTemp.ScratchAvailable) {
				t.Fatalf("broker=%+v settlement=%+v", broker, tool.Effects.Out.Facts.SharedTemp)
			}
			if tc.running && (tool.Effects.Out.Facts.SharedTemp == nil || !tool.Effects.Out.Facts.SharedTemp.Running) {
				t.Fatalf("running facts=%+v", tool.Effects.Out.Facts)
			}
		})
	}
}
