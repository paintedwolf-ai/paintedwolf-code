package wiring

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Entering a phase that sets an execution mode hands the coordinator's next
// turn a phase-hook transition cause; a phase without one hands it none.
func TestEnteringAWorkflowPhasePushesModeTransitionCause(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block string
		want  []surface.ModeTransitionCause
	}{
		{
			name:  "phase sets orchestrate",
			block: "    on_enter:\n      set_execution_mode: orchestrate\n",
			want:  []surface.ModeTransitionCause{{Kind: surface.ModeTransitionCausePhaseHook, Mode: surface.ExecutionModeFamilyOrchestrate}},
		},
		{name: "phase sets nothing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := BuildForTest(t)
			ctx := h.OwnerCtx(t, context.Background())
			m, err := workflowdef.ParseManifestYAML([]byte("id: mode-probe\nversion: 1.0.0\nname: Mode probe\ntrigger: /mode-probe\n" +
				"request:\n  question: What?\nphases:\n  - id: work\n    activity_label: Working\n" + tc.block))
			testutil.FailErr(t, "parse probe manifest", err)
			h.RegisterManifest(m)
			sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{}, t.TempDir())
			testutil.FailErr(t, "create session", err)
			run, err := h.Workflows.Manager.Starts.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{
				WorkflowID: "mode-probe", WorkflowVersion: "1.0.0", Request: "Probe the execution mode",
			})
			testutil.FailErr(t, "start probe run", err)
			if run.CurrentPhase != "work" {
				t.Fatalf("phase = %q want work", run.CurrentPhase)
			}
			rt := h.Sessions.Manager.Coordinator.Runtime
			rt.BeginPromptTurn(sess.ID)
			t.Cleanup(func() { rt.EndPromptTurn(sess.ID) })
			if got := rt.Assembly().Cache().LoadTurn(sess.ID).ModeTransitionCauses; !slices.Equal(got, tc.want) {
				t.Fatalf("turn mode causes = %+v want %+v", got, tc.want)
			}
		})
	}
}
