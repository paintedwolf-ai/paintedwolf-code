package delegation

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"testing"

	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAmbientGroundingCheckTurnNoJobsPass(t *testing.T) {
	cfg := DefaultGroundingConfig()
	gate := NewSimpleAmbientGroundingGate(cfg)
	in := AmbientGroundingInput{}
	v := gate.CheckTurn(context.Background(), in)
	if !v.OK {
		t.Fatalf("no worker jobs: prose must not trigger ambient grounding: %+v", v)
	}
}

func TestAmbientGroundingCheckTurnCompleteJobWithoutSummary(t *testing.T) {
	cfg := DefaultGroundingConfig()
	gate := NewSimpleAmbientGroundingGate(cfg)
	in := AmbientGroundingInput{
		Jobs: []api.WorkerTask{{
			ID:     "job-1",
			Status: api.WorkerStatusComplete,
		}},
	}
	v := gate.CheckTurn(context.Background(), in)
	if v.OK {
		t.Fatal("expected ledger violation")
	}
	if v.Code != ambientUngroundedCompletionCode {
		t.Fatalf("code = %q", v.Code)
	}
}

func TestAmbientGroundingCheckTurnTaskExempt(t *testing.T) {
	cfg := DefaultGroundingConfig()
	gate := NewSimpleAmbientGroundingGate(cfg)
	in := AmbientGroundingInput{
		Jobs: []api.WorkerTask{{
			ID:     "job-1",
			Status: api.WorkerStatusComplete,
		}},
		LastTurnTools: []string{"task"},
	}
	v := gate.CheckTurn(context.Background(), in)
	if !v.OK {
		t.Fatalf("task() turn should be exempt: %+v", v)
	}
}

func TestAmbientGroundingCheckTurnWaitExempt(t *testing.T) {
	cfg := DefaultGroundingConfig()
	gate := NewSimpleAmbientGroundingGate(cfg)
	in := AmbientGroundingInput{
		Jobs: []api.WorkerTask{{
			ID:     "job-1",
			Status: api.WorkerStatusComplete,
		}},
		LastTurnTools: []string{"wait"},
	}
	v := gate.CheckTurn(context.Background(), in)
	if !v.OK {
		t.Fatalf("wait() turn should be exempt: %+v", v)
	}
}

func TestAmbientGroundingCheckTurnRunningJobGrounded(t *testing.T) {
	cfg := DefaultGroundingConfig()
	gate := NewSimpleAmbientGroundingGate(cfg)
	in := AmbientGroundingInput{
		Jobs: []api.WorkerTask{{
			ID:     "job-1",
			Status: api.WorkerStatusRunning,
		}},
	}
	v := gate.CheckTurn(context.Background(), in)
	if !v.OK {
		t.Fatalf("running job should satisfy ledger: %+v", v)
	}
}

func TestAmbientLedgerSatisfiedInFlightAfterUnsummarizedComplete(t *testing.T) {
	if !ambientLedgerSatisfied([]api.WorkerTask{
		{ID: "job-1", Status: api.WorkerStatusComplete},
		{ID: "job-2", Status: api.WorkerStatusRunning},
	}, nil) {
		t.Fatal("a later in-flight job must satisfy the ledger regardless of job order")
	}
}

func TestAmbientGroundingCheckTurnSummaryTagGrounded(t *testing.T) {
	cfg := DefaultGroundingConfig()
	gate := NewSimpleAmbientGroundingGate(cfg)
	in := AmbientGroundingInput{
		Jobs: []api.WorkerTask{{
			ID:     "job-1",
			Status: api.WorkerStatusComplete,
		}},
		SummaryTags: []WorkerSummaryTag{{
			JobID: "job-1",
		}},
	}
	v := gate.CheckTurn(context.Background(), in)
	if !v.OK {
		t.Fatalf("worker summary tag should satisfy ledger: %+v", v)
	}
}

func TestAmbientGroundingCheckTurnNoWorkerUngroundedAudit(t *testing.T) {
	cfg := DefaultGroundingConfig()
	gate := NewSimpleAmbientGroundingGate(cfg)
	in := AmbientGroundingInput{
		LastAuditUngrounded: true,
		LastAuditCode:       "SYNTH_HANDLE_NOT_IN_LEGS",
	}
	v := gate.CheckTurn(context.Background(), in)
	if v.OK {
		t.Fatal("no-worker turn with an ungrounded citation audit must produce a verdict")
	}
	if v.Code != "SYNTH_HANDLE_NOT_IN_LEGS" {
		t.Fatalf("code = %q want the audit code carried through", v.Code)
	}
}

func TestAmbientGroundingCheckTurnNoWorkerGroundedPass(t *testing.T) {
	gate := NewSimpleAmbientGroundingGate(DefaultGroundingConfig())
	v := gate.CheckTurn(context.Background(), AmbientGroundingInput{LastAuditUngrounded: false})
	if !v.OK {
		t.Fatalf("no-worker turn with a grounded (or absent) audit must pass: %+v", v)
	}
}

func TestAmbientGroundingNeverEscalatesUnderDefaultConfig(t *testing.T) {
	cfg := DefaultGroundingConfig()
	state := grounding.UngroundedCounter{}
	verdict := GroundingVerdict{OK: false, Code: ambientUngroundedCompletionCode}
	for i := 0; i < 10; i++ {
		ApplyCircuitBreaker(&state, verdict, cfg)
	}
	if state.Escalated {
		t.Fatal("default (flag) escalate mode must never hard-block the session")
	}
	if !groundingFlagged(state, cfg) {
		t.Fatal("accumulated warnings past threshold must raise an advisory flag")
	}
}

func TestAmbientGroundingAfterPromptSkipsDelegationSession(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	sessID := "sess-delegation"
	leg := api.Leg{ID: "leg-1", Title: "t", Prompt: "do work"}
	delegation := api.Delegation{ID: "dep-1", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: t.TempDir(), Task: "do work", Phase: api.DelegationPhaseWorker}
	if _, err := store.Create(ctx, delegation, sessID, []api.Leg{leg}); err != nil {
		testutil.FailErr(t, "create delegation", err)
	}

	gate := NewSimpleAmbientGroundingGate(DefaultGroundingConfig())
	coord := NewAmbientGroundingCoordinator(store, nil, gate, DefaultGroundingConfig(), grounding.NewStateStore(), nil)
	err := coord.AfterPrompt(ctx, sessID, nil)
	testutil.FailErr(t, "AfterPrompt delegation skip", err)
}

func TestAmbientLedgerSatisfiedCompleteJobNeedsSummary(t *testing.T) {
	if ambientLedgerSatisfied([]api.WorkerTask{{
		ID:     "job-1",
		Status: api.WorkerStatusComplete,
	}}, nil) {
		t.Fatal("complete job without summary tag should not satisfy ledger")
	}
}

func TestAmbientLedgerSatisfiedFailedJobPass(t *testing.T) {
	if !ambientLedgerSatisfied([]api.WorkerTask{{
		ID:     "job-1",
		Status: api.WorkerStatusFailed,
	}}, nil) {
		t.Fatal("failed job with no complete work should satisfy ledger")
	}
}
