package command

import (
	"context"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	diagPageSize     = 100
	diagMaxPages     = 3
	diagMaxPinChecks = 16
)

// observeSettledProgramDiagnostics reads bounded receipt metadata from the
// existing scoped ProgramReader and submits an immutable snapshot to the
// opt-in async diagnostics queue. It never alters product state or returns
// errors to the caller.
func observeSettledProgramDiagnostics(ctx context.Context, recorder *invocation.SQLRecorder, sessionID, toolCallID, programID string) {
	if recorder == nil || !cmddiag.Enabled() {
		return
	}
	readCtx := context.WithoutCancel(ctx)
	var (
		program api.CommandProgramReceipt
		execs   []cmddiag.ExecutionFact
		links   []cmddiag.OperationSourceLink
		cursor  string
		omitted int
		trunc   bool
	)
	pinChecks := 0
	for pageNum := 0; pageNum < diagMaxPages; pageNum++ {
		page, err := recorder.ReadProgramPage(readCtx, sessionID, programID, cursor, diagPageSize)
		if err != nil {
			return
		}
		if pageNum == 0 {
			program = page.Program
		}
		for _, ex := range page.Executions {
			execs = append(execs, executionFactFromReceipt(ex))
			if ex.EffectCertainty == "completed" && ex.PublishedRevision != "" && pinChecks < diagMaxPinChecks {
				pinChecks++
				links = append(links, linkedOperationFact(readCtx, recorder, sessionID, ex))
			}
		}
		omitted = page.ExecutionsOmitted
		if page.NextCursor == "" || omitted == 0 {
			break
		}
		if pageNum == diagMaxPages-1 {
			trunc = true
			break
		}
		cursor = page.NextCursor
	}

	var pres *cmddiag.PresentationSnapshot
	caughtUp := false
	if summary, err := recorder.ActivitySummary(readCtx, sessionID, programID); err == nil {
		caughtUp = true
		pres = presentationFromReceipt(summary)
	}

	cmddiag.Observe(cmddiag.Observation{
		SessionID:         sessionID,
		ProgramID:         program.ID,
		ParentReceiptID:   program.ParentReceiptID,
		ToolCallID:        toolCallID,
		ProgramState:      program.State,
		ProgramStatusCode: program.StatusCode,
		ProgramStopCode:   program.StopCode,
		ProgramSettled:    program.SettledAt != nil && cmddiag.IsTerminalState(program.State),
		Truncated:         trunc,
		ExecutionsOmitted: omitted,
		ActivityRevision:  program.ActivityRevision,
		ConsumerCaughtUp:  caughtUp,
		Presentation:      pres,
		Executions:        execs,
		LinkedOperations:  links,
	})
}

func executionFactFromReceipt(ex api.CommandExecutionReceipt) cmddiag.ExecutionFact {
	opID := ex.NativeCommand
	if opID == "" {
		opID = ex.Owner
	}
	return cmddiag.ExecutionFact{
		ExecutionID:       ex.ID,
		ParentExecutionID: ex.ParentExecutionID,
		OperationID:       opID,
		Owner:             ex.Owner,
		State:             ex.State,
		Admitted:          isAdmittedExecution(ex),
		OwnerStarted:      ex.OwnerStarted,
		OwnerInvoked:      ex.OwnerInvoked,
		StatusCode:        ex.StatusCode,
		StopCode:          ex.StopCode,
		HostStopCode:      ex.HostStopCode,
		EffectCertainty:   ex.EffectCertainty,
		SourceRef:         cmddiag.RedactReference(ex.DetailRef),
		AcceptedRevision:  ex.AcceptedRevision,
		PublishedRevision: ex.PublishedRevision,
	}
}

func isAdmittedExecution(ex api.CommandExecutionReceipt) bool {
	if ex.State != "prepared" && ex.State != "refused" {
		return true
	}
	return ex.OwnerStarted || ex.ApprovalRef != "" ||
		ex.EffectCertainty == "completed" ||
		ex.EffectCertainty == "unknown" ||
		ex.EffectCertainty == "attempted"
}

func linkedOperationFact(ctx context.Context, recorder *invocation.SQLRecorder, sessionID string, ex api.CommandExecutionReceipt) cmddiag.OperationSourceLink {
	opID := ex.NativeCommand
	if opID == "" {
		opID = ex.Owner
	}
	link := cmddiag.OperationSourceLink{
		ExecutionID:         ex.ID,
		OperationID:         opID,
		ExpectedOperationID: opID,
		Completed:           true,
	}
	pins, err := recorder.RevisionPins(ctx, sessionID, ex.ID)
	if err != nil {
		return link
	}
	for _, pin := range pins {
		if pin.Role == toolcontract.ExecutionRevisionDestination {
			src := cmddiag.RedactReference(pin.RootID + ":" + pin.Path)
			link.SourceID = src
			link.ExpectedSourceID = src
			link.ExpectedRevision = ex.PublishedRevision
			link.ObservedRevision = ex.PublishedRevision
			link.CompleteEvidence = true
			return link
		}
	}
	// Completed mutation without destination pin is left incomplete rather than guessed.
	return link
}

func presentationFromReceipt(summary api.CommandProgramReceipt) *cmddiag.PresentationSnapshot {
	acts := make([]cmddiag.ActivityFact, 0, len(summary.Activities))
	for _, a := range summary.Activities {
		acts = append(acts, cmddiag.ActivityFact{
			ActivityID:   a.ID,
			State:        a.State,
			StatusCode:   a.StatusCode,
			ExecutionIDs: append([]string(nil), a.ExecutionIDs...),
		})
	}
	return &cmddiag.PresentationSnapshot{
		ProgramState:      summary.State,
		ProgramStatusCode: summary.StatusCode,
		ProgramStopCode:   summary.StopCode,
		ActivityRevision:  summary.ActivityRevision,
		Activities:        acts,
	}
}
