package command

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	diagPageSize     = 100
	diagMaxPages     = 3
	diagMaxPinChecks = 16
)

type programDiagnosticReader interface {
	ReadProgramPage(ctx context.Context, sessionID, programID, cursor string, limit int) (api.CommandProgramReceiptPage, error)
	RevisionPins(ctx context.Context, sessionID, executionID string) ([]toolcontract.ExecutionRevision, error)
}

// observeSettledProgramDiagnostics enqueues a lightweight bounded task onto
// the async diagnostics queue WITHOUT performing any store or receipt reads on
// the caller goroutine. When the queue is saturated, no receipt reads occur.
func observeSettledProgramDiagnostics(reader programDiagnosticReader, sessionID, toolCallID, programID string, delivered *api.CommandProgramReceipt) bool {
	if reader == nil || !cmddiag.Enabled() {
		return false
	}
	var pres *cmddiag.PresentationSnapshot
	if delivered != nil {
		pres = presentationFromReceipt(*delivered)
	}
	return cmddiag.ObserveTask(cmddiag.Task{
		SessionID:  sessionID,
		ProgramID:  programID,
		ToolCallID: toolCallID,
		Fetch: func(ctx context.Context) (cmddiag.Observation, error) {
			return fetchProgramObservation(ctx, reader, sessionID, toolCallID, programID, pres)
		},
	})
}

func fetchProgramObservation(ctx context.Context, reader programDiagnosticReader, sessionID, toolCallID, programID string, delivered *cmddiag.PresentationSnapshot) (cmddiag.Observation, error) {
	var (
		program          api.CommandProgramReceipt
		execs            []cmddiag.ExecutionFact
		links            []cmddiag.OperationSourceLink
		lookupFailed     []string
		cursor           string
		omitted          int
		trunc            bool
		pageInconsistent bool
	)
	seenExecs := make(map[string]struct{})
	pinChecks := 0
	for pageNum := 0; pageNum < diagMaxPages; pageNum++ {
		if err := ctx.Err(); err != nil {
			return cmddiag.Observation{}, err
		}
		page, err := reader.ReadProgramPage(ctx, sessionID, programID, cursor, diagPageSize)
		if err != nil {
			return cmddiag.Observation{}, fmt.Errorf("read program page %d: %w", pageNum, err)
		}
		if pageNum == 0 {
			program = page.Program
		} else if page.Program.ID != program.ID || page.Program.State != program.State {
			pageInconsistent = true
			break
		}
		for _, ex := range page.Executions {
			if _, dup := seenExecs[ex.ID]; dup {
				pageInconsistent = true
				continue
			}
			seenExecs[ex.ID] = struct{}{}
			execs = append(execs, executionFactFromReceipt(ex))
			if ex.EffectCertainty == "completed" && ex.PublishedRevision != "" && pinChecks < diagMaxPinChecks {
				pinChecks++
				link, failed := linkedOperationFact(ctx, reader, sessionID, ex)
				if failed {
					lookupFailed = append(lookupFailed, ex.ID)
				} else {
					links = append(links, link)
				}
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

	return cmddiag.Observation{
		SessionID:               sessionID,
		ProgramID:               program.ID,
		ParentReceiptID:         program.ParentReceiptID,
		ToolCallID:              toolCallID,
		ProgramState:            program.State,
		ProgramStatusCode:       program.StatusCode,
		ProgramStopCode:         program.StopCode,
		ProgramSettled:          program.SettledAt != nil && cmddiag.IsTerminalState(program.State),
		Truncated:               trunc,
		ExecutionsOmitted:       omitted,
		PageInconsistent:        pageInconsistent,
		LookupFailedExecutions:  lookupFailed,
		ActivityRevision:        program.ActivityRevision,
		ConsumerCaughtUp:        delivered != nil,
		PresentationUnsupported: delivered == nil,
		Presentation:            delivered,
		Executions:              execs,
		LinkedOperations:        links,
	}, nil
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
		SourceRef:         cmddiag.DigestReference(ex.DetailRef),
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

// linkedOperationFact extracts an opaque source digest from the execution's
// destination revision pin without comparing fields against themselves.
// Because ProgramReader does not expose an independent post-write owner record,
// completed mutations disclose an explicit unsupported disposition rather than
// reporting a synthetic clean match.
func linkedOperationFact(ctx context.Context, reader programDiagnosticReader, sessionID string, ex api.CommandExecutionReceipt) (cmddiag.OperationSourceLink, bool) {
	opID := ex.NativeCommand
	if opID == "" {
		opID = ex.Owner
	}
	link := cmddiag.OperationSourceLink{
		ExecutionID: ex.ID,
		OperationID: opID,
		Completed:   true,
	}
	pins, err := reader.RevisionPins(ctx, sessionID, ex.ID)
	if err != nil {
		return link, true
	}
	for _, pin := range pins {
		if pin.Role == toolcontract.DestinationRevision {
			link.SourceID = cmddiag.DigestReference(pin.RootID, pin.Path)
			link.UnsupportedReason = "no_independent_post_write_owner_record"
			return link, false
		}
	}
	return link, false
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
