package command

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/pkg/api"
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
	detailRef := "output:program-source:" + run.root.Identity.ProgramID
	if err != nil {
		return nil, settleFailedProgramProgress(settleCtx, r, run, res, detailRef, err)
	}
	if run.tc.Effects.Out != nil {
		advanced := res.Outcome.AdvancesWorkflow()
		run.tc.Effects.Out.Facts.WorkflowAdvanced = &advanced
	}
	receiptState := "unchanged"
	status := "unchanged"
	if res.Outcome.Updated {
		receiptState = "committed"
		status = "updated"
	}
	if settleErr := r.Recorder.SettleProgramProgress(settleCtx, invocation.ProgressSettlement{
		Identity:        res.Identity,
		OwnerInvoked:    true,
		State:           receiptState,
		DetailReference: "output:progress:" + res.Identity.ExecutionID,
	}); settleErr != nil {
		return nil, recoverCommittedProgressSettlementFault(settleCtx, r, run, res, detailRef, settleErr)
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

func settleFailedProgramProgress(
	ctx context.Context,
	r *ProgramRuntime,
	run *programRun,
	res toolexecution.ProgramProgressResult,
	detailRef string,
	applyErr error,
) error {
	stopCode := toolrejection.ToolOwnerFailedCode
	state := "unknown"
	tr := toolrejection.AsToolReject(applyErr)
	if tr != nil {
		if tr.Code != "" {
			stopCode = tr.Code
		}
		state = "rejected"
	}
	var settleErrs []error
	if res.Identity.ExecutionID != "" {
		if sErr := r.Recorder.SettleProgramProgress(ctx, invocation.ProgressSettlement{
			Identity:        res.Identity,
			OwnerInvoked:    res.OwnerInvoked,
			State:           state,
			DetailReference: "output:progress:" + res.Identity.ExecutionID,
		}); sErr != nil {
			settleErrs = append(settleErrs, fmt.Errorf("settle progress receipt: %w", sErr))
		}
	} else if rErr := r.Recorder.RecordProgressRefusal(ctx, run.root.Identity.ProgramID, "progress"); rErr != nil {
		settleErrs = append(settleErrs, fmt.Errorf("record progress refusal: %w", rErr))
	}
	if pErr := r.Recorder.SettleProgram(ctx, run.root.Identity.ProgramID, toolcontract.ProgramSettlement{
		State:           "rejected",
		StopCode:        stopCode,
		DetailReference: detailRef,
	}); pErr != nil {
		settleErrs = append(settleErrs, fmt.Errorf("settle program receipt: %w", pErr))
		if iErr := r.Recorder.InterruptProgram(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID, detailRef); iErr != nil {
			settleErrs = append(settleErrs, fmt.Errorf("recover program receipt: %w", iErr))
		}
	}
	if len(settleErrs) == 0 {
		return applyErr
	}
	joined := errors.Join(settleErrs...)
	if tr == nil {
		return errors.Join(applyErr, joined)
	}
	clone := *tr
	data := map[string]any{}
	if tr.Data != nil {
		maps.Copy(data, tr.Data)
	}
	data["program_id"] = run.root.Identity.ProgramID
	data["progress_committed"] = false
	data["command_executed"] = false
	data["program_replay_available"] = false
	data["settlement_error"] = joined.Error()
	clone.Data = data
	return &clone
}

func recoverCommittedProgressSettlementFault(
	ctx context.Context,
	r *ProgramRuntime,
	run *programRun,
	res toolexecution.ProgramProgressResult,
	detailRef string,
	settleErr error,
) error {
	recoverErr := r.Recorder.InterruptProgram(ctx, run.tc.Identity.SessionID, run.root.Identity.ProgramID, detailRef)
	data := map[string]any{
		"tool":                     run.tool,
		"program_id":               run.root.Identity.ProgramID,
		"progress_execution_id":    res.Identity.ExecutionID,
		"progress_committed":       res.Outcome.Updated,
		"progress_owner_invoked":   true,
		"command_executed":         false,
		"program_replay_available": false,
		"reason":                   settleErr.Error(),
	}
	if res.Outcome.Updated {
		data["snapshot"] = progress.ForRequest(res.Identity.RootSessionID, res.Outcome.Snapshot)
	}
	if recoverErr != nil {
		data["settlement_error"] = recoverErr.Error()
	}
	return &toolrejection.ToolReject{
		Code:         toolrejection.ToolOwnerFailedCode,
		OwnerRef:     "progress",
		FailureClass: api.FailureClassOwnerError,
		BeforeEffect: !res.Outcome.Updated,
		Data:         data,
	}
}
