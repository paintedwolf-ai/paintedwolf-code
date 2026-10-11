package workflow

import (
	"context"
	"errors"
	"math/rand"
	"testing"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// TestSessionWalkPropertyInvariants checks run state after fixed-seed operations.
func TestSessionWalkPropertyInvariants(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(20260531))

	sessionID := "sess-1"
	const steps = 50

	type op string
	const (
		opStart   op = "start"
		opAdvance op = "advance"
		opPause   op = "pause"
		opResume  op = "resume"
		opExit    op = "exit"
		opCancel  op = "cancel"
	)
	allOps := []op{opStart, opAdvance, opPause, opResume, opExit, opCancel}

	var planSeeded bool

	for step := 0; step < steps; step++ {
		choice := allOps[rng.Intn(len(allOps))]
		active, err := mgr.Store.Runs.ActiveBySession(ctx, sessionID)
		if err != nil {
			t.Fatalf("step %d: ActiveBySession err: %v", step, err)
		}
		// Reset seeded flag when there's no active run (next Start gets a fresh plan).
		if active == nil {
			planSeeded = false
		}

		switch choice {
		case opStart:
			run, err := mgr.Starts.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
				WorkflowID: "plan", WorkflowVersion: "1.0.0", Request: "test request",
			})
			if err != nil {
				t.Fatalf("step %d Start unexpected err: %v (active=%v)", step, err, active)
			}
			if run == nil {
				t.Fatalf("step %d Start returned nil run, no error", step)
			}
			seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
			planSeeded = true

		case opAdvance:
			if active == nil {
				continue
			}
			if !planSeeded {
				// Without a seeded plan the gate is unmet; that's a legal
				// error path, not a property violation.
				continue
			}
			_, err := mgr.Phases.Advance(ctx, active.ID)
			if err == nil {
				continue
			}
			if _, ok := runstate.IsPhaseGateUnmet(err); ok {
				continue // expected when the gate isn't satisfied yet
			}
			if errors.Is(err, workflowdef.ErrUnknownWorkflow) {
				continue
			}
			if _, ok := runstate.IsNotRunnable(err); ok {
				continue
			}
			t.Fatalf("step %d Advance unexpected err: %v", step, err)

		case opPause:
			if active == nil {
				continue
			}
			run, err := mgr.Controls.Pause(ctx, active.ID, "test")
			if err != nil {
				if _, ok := runstate.IsNotRunnable(err); ok {
					continue
				}
				t.Fatalf("step %d Pause unexpected err: %v", step, err)
			}
			if run.Status != api.WorkflowRunStatusPaused {
				t.Fatalf("step %d Pause produced status %q want paused", step, run.Status)
			}

		case opResume:
			if active == nil {
				continue
			}
			run, err := mgr.Controls.Resume(ctx, active.ID)
			if err != nil {
				if _, ok := runstate.IsNotRunnable(err); ok {
					continue
				}
				t.Fatalf("step %d Resume unexpected err: %v", step, err)
			}
			if run.Status != api.WorkflowRunStatusRunning {
				t.Fatalf("step %d Resume produced status %q want running", step, run.Status)
			}

		case opExit:
			if active == nil {
				continue
			}
			exitedCatalog := false
			if m, merr := mgr.Resolver.ForRun(ctx, active); merr == nil {
				exitedCatalog = m.IsCatalogVisible()
			}
			_, err := mgr.Controls.Exit(ctx, sessionID, active.ID, active.Revision, "test")
			if err != nil {
				t.Fatalf("step %d Exit unexpected err: %v", step, err)
			}
			af, err := mgr.Store.Runs.ActiveBySession(ctx, sessionID)
			if err != nil {
				t.Fatalf("step %d post-Exit ActiveBySession err: %v", step, err)
			}
			if exitedCatalog {
				if af == nil || af.WorkflowID != "implement" || runstate.IsTerminal(af.Status) {
					t.Fatalf("step %d post-catalog-Exit want fresh ambient, got %+v", step, af)
				}
			} else if af != nil {
				t.Fatalf("step %d post-Exit ActiveBySession non-nil: %+v", step, af)
			}

		case opCancel:
			if active == nil {
				continue
			}
			run, err := mgr.Controls.Cancel(ctx, active.ID, "test")
			if err != nil {
				if _, ok := runstate.IsNotRunnable(err); ok {
					continue
				}
				t.Fatalf("step %d Cancel unexpected err: %v", step, err)
			}
			if run.Status != api.WorkflowRunStatusCanceled {
				t.Fatalf("step %d Cancel produced status %q want canceled", step, run.Status)
			}
		}

		// Post-step global invariants.
		af, err := mgr.Store.Runs.ActiveBySession(ctx, sessionID)
		if err != nil {
			t.Fatalf("step %d invariant probe err: %v", step, err)
		}
		if af != nil {
			// Active runs are never terminal (ActiveBySession filters them).
			if runstate.IsTerminal(af.Status) {
				t.Fatalf("step %d invariant: ActiveBySession returned terminal run %+v", step, af)
			}
		}
	}
}
