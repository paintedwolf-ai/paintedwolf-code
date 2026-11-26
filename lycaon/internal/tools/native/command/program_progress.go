package command

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// ProgramProgressOwner supplies the existing progress store and reconciler
// when a command call includes an optional progress_update payload.
type ProgramProgressOwner interface {
	ProgramProgressStore() progress.Store
	ProgramProgressReconciler() progress.Reconciler
}

// ProgramProgressOutcome reports the independently settled progress_update
// result in the same shape as standalone update_progress.
type ProgramProgressOutcome struct {
	Status               string                   `json:"status"`
	DocumentChanged      bool                     `json:"document_changed"`
	StateChanged         bool                     `json:"state_changed"`
	ClosedItemIDs        []string                 `json:"closed_item_ids,omitempty"`
	ReconciliationStatus string                   `json:"reconciliation_status,omitempty"`
	Snapshot             progress.RequestSnapshot `json:"snapshot"`
}

func extractProgramProgressUpdate(tool string, args map[string]any) (*toolexecution.ProgramProgressUpdate, error) {
	raw, exists := args["progress_update"]
	if !exists || raw == nil {
		return nil, nil
	}
	if tool != "command" {
		return nil, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"field": "progress_update"})
	}
	payload, ok := raw.(map[string]any)
	if !ok {
		return nil, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"field": "progress_update"})
	}
	return &toolexecution.ProgramProgressUpdate{
		ExecutionID: uuid.NewString(),
		Arguments:   payload,
	}, nil
}

func prepareProgramProgress(
	ctx context.Context,
	r *ProgramRuntime,
	run *programRun,
	parsed toolexecution.ValidatedProgramIdentity,
	update *toolexecution.ProgramProgressUpdate,
) (*toolexecution.PreparedProgramEnvelope, progress.Store, progress.Reconciler, error) {
	if update == nil {
		return nil, nil, nil, nil
	}
	owner, ok := r.Progress.(ProgramProgressOwner)
	if !ok || owner == nil || owner.ProgramProgressStore() == nil {
		return nil, nil, nil, fmt.Errorf("progress owner unavailable")
	}
	prepared, err := r.Executor.PrepareProgramEnvelope(ctx, run.frame, parsed, update, run.memory, run.tc)
	if err != nil {
		return nil, nil, nil, err
	}
	return prepared, owner.ProgramProgressStore(), owner.ProgramProgressReconciler(), nil
}

func applyProgramProgress(
	ctx context.Context,
	r *ProgramRuntime,
	run *programRun,
	prepared *toolexecution.PreparedProgramEnvelope,
	store progress.Store,
	reconciler progress.Reconciler,
) (*ProgramProgressOutcome, error) {
	if prepared == nil {
		return nil, nil
	}
	defer prepared.Close()
	observer := invocation.ProgramAdmissionRecorder{Recorder: r.Recorder, Inputs: run.capture}
	res, err := r.Executor.ApplyProgramProgress(ctx, prepared, store, r.Progress, reconciler, observer, run.tc)
	settleCtx := context.WithoutCancel(ctx)
	if err != nil {
		stopCode := toolrejection.ToolOwnerFailedCode
		state := "unknown"
		if tr := toolrejection.AsToolReject(err); tr != nil {
			if tr.Code != "" {
				stopCode = tr.Code
			}
			state = "rejected"
		}
		if res.Identity.ExecutionID != "" {
			_ = r.Recorder.SettleProgramProgress(settleCtx, invocation.ProgressSettlement{
				Identity:        res.Identity,
				OwnerInvoked:    res.OwnerInvoked,
				State:           state,
				DetailReference: "output:progress:" + res.Identity.ExecutionID,
			})
		} else {
			_ = r.Recorder.RecordProgressRefusal(settleCtx, run.root.Identity.ProgramID, "progress")
		}
		_ = r.Recorder.SettleProgram(settleCtx, run.root.Identity.ProgramID, toolcontract.ProgramSettlement{
			State:           "rejected",
			StopCode:        stopCode,
			DetailReference: "output:program-source:" + run.root.Identity.ProgramID,
		})
		return nil, err
	}
	receiptState := "unchanged"
	status := "unchanged"
	if res.Outcome.Updated {
		receiptState = "committed"
		status = "updated"
	}
	if err := r.Recorder.SettleProgramProgress(settleCtx, invocation.ProgressSettlement{
		Identity:        res.Identity,
		OwnerInvoked:    true,
		State:           receiptState,
		DetailReference: "output:progress:" + res.Identity.ExecutionID,
	}); err != nil {
		return nil, err
	}
	if run.tc.Effects.Out != nil {
		advanced := res.Outcome.AdvancesWorkflow()
		run.tc.Effects.Out.Facts.WorkflowAdvanced = &advanced
	}
	return &ProgramProgressOutcome{
		Status:               status,
		DocumentChanged:      res.Outcome.Updated,
		StateChanged:         res.Outcome.StateChanged,
		ClosedItemIDs:        res.Outcome.ClosedItemIDs,
		ReconciliationStatus: res.Outcome.ReconciliationStatus,
		Snapshot:             progress.ForRequest(res.Identity.RootSessionID, res.Outcome.Snapshot),
	}, nil
}
