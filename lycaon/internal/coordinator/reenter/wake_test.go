package reenter_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/reenter"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

type recordingNudger struct {
	legFinished int
}

func (r *recordingNudger) Nudge(_ context.Context, _ string, wake, _ anchor.ID, _ string, _ anchor.Envelope) {
	if wake == anchor.LegFinished {
		r.legFinished++
	}
}

func TestNudgeOnManifestReenterSkipsWorkerTaskFinishedInject(t *testing.T) {
	n := &recordingNudger{}
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{
		ID: "work",
		OnReenter: workflowdef.PhaseOnReenter{
			InjectKick: anchor.InformRender(anchor.WorkerTaskFinished),
			ReenterLeg: "implement-work:{session_id}",
		},
	}}}
	reenter.NudgeOnManifestReenter(context.Background(), n, "sess-1", manifest, "work", "work")
	if n.legFinished != 0 {
		t.Fatalf("leg-finished nudges = %d want 0 (bridge triggers worker wake)", n.legFinished)
	}
}

func TestNudgeOnManifestReenterLegFinished(t *testing.T) {
	n := &recordingNudger{}
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{
		ID:        "work",
		OnReenter: workflowdef.PhaseOnReenter{ReenterLeg: "leg:{session_id}"},
	}}}
	reenter.NudgeOnManifestReenter(context.Background(), n, "sess-1", manifest, "work", "work")
	if n.legFinished != 1 {
		t.Fatalf("leg-finished nudges = %d want 1", n.legFinished)
	}
}
