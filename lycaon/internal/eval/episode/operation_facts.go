package episode

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/pkg/api"
)

// Settlement says whether the host recorded a terminal state for an operation.
type Settlement string

// SemanticOutcome says whether an operation did the work it was asked to do.
// A completed transport is not success: a program can run and fail.
type SemanticOutcome string

// EffectState says what the host knows about an operation's effects.
type EffectState string

const (
	Settled   Settlement = "settled"
	Unsettled Settlement = "unsettled"

	OutcomeSuccess SemanticOutcome = "success"
	OutcomeFailure SemanticOutcome = "failure"
	OutcomeUnknown SemanticOutcome = "unknown"

	// EffectsNone means the operation could not have changed anything.
	EffectsNone EffectState = "none"
	// EffectsKnown means every effect, if any, was recorded.
	EffectsKnown EffectState = "known"
	// EffectsUnknown means an effect may have happened without a record.
	EffectsUnknown EffectState = "unknown"
)

// Operation is one normalized unit of host work. Child operations name their
// parent; a failed parent keeps its settled children and their effects.
type Operation struct {
	ID              string          `json:"id"`
	ParentID        string          `json:"parent_id,omitempty"`
	SessionID       string          `json:"session_id"`
	MessageID       string          `json:"message_id,omitempty"`
	ToolCallID      string          `json:"tool_call_id,omitempty"`
	Tool            string          `json:"tool,omitempty"`
	ArgsDigest      string          `json:"args_sha256,omitempty"`
	Source          string          `json:"source"`
	Contract        string          `json:"contract"`
	Invoked         bool            `json:"invoked"`
	Settlement      Settlement      `json:"settlement"`
	SemanticOutcome SemanticOutcome `json:"semantic_outcome"`
	EffectState     EffectState     `json:"effect_state"`
	FailureCode     string          `json:"failure_code,omitempty"`
	// SourceVerdict is the verification verdict the host recorded for the
	// source at SourceRevision; empty when the operation verified nothing.
	SourceVerdict  string     `json:"source_verdict,omitempty"`
	SourceRevision string     `json:"source_revision,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	SettledAt      *time.Time `json:"settled_at,omitempty"`
}

// SourceIdentity names an operation evidence source and the contract its
// operations follow; the training contract lists every registered one.
type SourceIdentity struct {
	Source   string `json:"source"`
	Contract string `json:"contract"`
}

// OperationEvidenceSource turns one invocation receipt into operations. A
// source applies by the evidence the host recorded, never by tool name.
type OperationEvidenceSource interface {
	Identity() SourceIdentity
	// Operations returns nil when the receipt carries none of this source's evidence.
	Operations(ctx context.Context, receipt api.InvocationReceipt, sessionID, messageID string) ([]Operation, error)
}

// programReads is the part of the invocation recorder the program source reads.
type programReads interface {
	ProgramForReceipt(ctx context.Context, sessionID, receiptID string) (string, error)
	ReadProgramPage(ctx context.Context, sessionID, programID, cursor string, limit int) (api.CommandProgramReceiptPage, error)
}

// operationSources lists every registered source; a receipt's operations come
// from the most specific source that applies, in this order.
func operationSources(reads programReads) []OperationEvidenceSource {
	return []OperationEvidenceSource{programSource{reads}, receiptSource{}}
}

// SourceIdentities lists the registered sources for the training contract.
func SourceIdentities() []SourceIdentity {
	var out []SourceIdentity
	for _, source := range operationSources(nil) {
		out = append(out, source.Identity())
	}
	return out
}

// receiptSource normalizes any invocation receipt from its own status.
type receiptSource struct{}

func (receiptSource) Identity() SourceIdentity {
	return SourceIdentity{Source: "invocation-receipt", Contract: "pw-operation-receipt/1"}
}

func (s receiptSource) Operations(_ context.Context, receipt api.InvocationReceipt, sessionID, messageID string) ([]Operation, error) {
	op := receiptOperation(s.Identity(), receipt, sessionID, messageID)
	switch {
	case receipt.Status == api.InvocationStatusCompleted && receipt.SourceVerdict != "" && receipt.SourceVerdict != "passed":
		op.SemanticOutcome, op.FailureCode = OutcomeFailure, "source_verdict_"+receipt.SourceVerdict
	case receipt.Status == api.InvocationStatusCompleted:
		op.SemanticOutcome = OutcomeSuccess
	case receipt.Status == api.InvocationStatusRejected || receipt.Status == api.InvocationStatusError:
		op.SemanticOutcome = OutcomeFailure
	}
	if receipt.Failure != nil && op.FailureCode == "" {
		op.FailureCode = receipt.Failure.Code
	}
	switch {
	case !receipt.Invoked:
		op.EffectState = EffectsNone
	case op.Settlement == Settled && receipt.Status != api.InvocationStatusInterrupted:
		op.EffectState = EffectsKnown
	}
	return []Operation{op}, nil
}

func receiptOperation(identity SourceIdentity, receipt api.InvocationReceipt, sessionID, messageID string) Operation {
	op := Operation{
		ID: receipt.ID, SessionID: sessionID, MessageID: messageID, ToolCallID: receipt.ToolCallID,
		Tool: receipt.Tool, ArgsDigest: receipt.ArgsDigest, Source: identity.Source, Contract: identity.Contract,
		Invoked: receipt.Invoked, Settlement: Unsettled, SemanticOutcome: OutcomeUnknown, EffectState: EffectsUnknown,
		SourceVerdict: receipt.SourceVerdict, SourceRevision: receipt.SourceRevision, StartedAt: receipt.StartedAt, SettledAt: receipt.SettledAt,
	}
	if receipt.SettledAt != nil {
		op.Settlement = Settled
	}
	return op
}

// programSource normalizes a receipt that launched a command program into the
// program operation and one child operation per execution.
type programSource struct{ reads programReads }

func (programSource) Identity() SourceIdentity {
	return SourceIdentity{Source: "command-program", Contract: "pw-operation-program/1"}
}

const programPageLimit = 100

func (s programSource) Operations(ctx context.Context, receipt api.InvocationReceipt, sessionID, messageID string) ([]Operation, error) {
	programID, err := s.reads.ProgramForReceipt(ctx, sessionID, receipt.ID)
	if errors.Is(err, invocation.ErrProgramNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var program api.CommandProgramReceipt
	var executions []api.CommandExecutionReceipt
	for cursor := ""; ; {
		page, err := s.reads.ReadProgramPage(ctx, sessionID, programID, cursor, programPageLimit)
		if err != nil {
			return nil, fmt.Errorf("program %s: %w", programID, err)
		}
		program, executions = page.Program, append(executions, page.Executions...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	identity := s.Identity()
	parent := receiptOperation(identity, receipt, sessionID, messageID)
	parent.SettledAt = program.SettledAt
	parent.Settlement = Unsettled
	children := make([]Operation, 0, len(executions))
	allSettled, allSucceeded := true, true
	effects := EffectsNone
	if program.ProgressState == "unknown" {
		effects = EffectsUnknown
	}
	for _, execution := range executions {
		child := executionOperation(identity, execution, receipt.ID, sessionID)
		allSettled = allSettled && child.Settlement == Settled
		allSucceeded = allSucceeded && child.SemanticOutcome == OutcomeSuccess
		effects = combineEffects(effects, child.EffectState)
		children = append(children, child)
	}
	if program.SettledAt != nil && allSettled {
		parent.Settlement = Settled
	}
	parent.EffectState = effects
	switch {
	case program.State == "completed" && program.StatusCode != nil && *program.StatusCode == 0 && allSucceeded && program.ProgressState != "unknown":
		parent.SemanticOutcome = OutcomeSuccess
	case program.State == "completed" || program.State == "failed" || program.State == "rejected":
		parent.SemanticOutcome, parent.FailureCode = OutcomeFailure, program.StopCode
	default:
		parent.SemanticOutcome, parent.FailureCode = OutcomeUnknown, program.StopCode
	}
	return append([]Operation{parent}, children...), nil
}

func executionOperation(identity SourceIdentity, execution api.CommandExecutionReceipt, parentID, sessionID string) Operation {
	op := Operation{
		ID: execution.ID, ParentID: parentID, SessionID: sessionID, Tool: execution.NativeCommand,
		ArgsDigest: execution.ArgumentsDigest, Source: identity.Source, Contract: identity.Contract,
		Invoked: execution.OwnerInvoked, Settlement: Unsettled, SemanticOutcome: OutcomeUnknown,
		FailureCode: execution.StopCode, SourceRevision: execution.PublishedRevision,
	}
	switch execution.State {
	case "completed", "refused", "failed", "interrupted":
		op.Settlement = Settled
	}
	switch {
	case execution.State == "completed" && execution.StatusCode != nil && *execution.StatusCode == 0:
		op.SemanticOutcome, op.FailureCode = OutcomeSuccess, ""
	case execution.State == "completed" || execution.State == "refused" || execution.State == "failed":
		op.SemanticOutcome = OutcomeFailure
	}
	switch execution.EffectCertainty {
	case "none":
		op.EffectState = EffectsNone
	case "completed":
		op.EffectState = EffectsKnown
	default:
		op.EffectState = EffectsUnknown
	}
	return op
}

// combineEffects keeps the least certain state: unknown, then known, then none.
func combineEffects(a, b EffectState) EffectState {
	switch {
	case a == EffectsUnknown || b == EffectsUnknown:
		return EffectsUnknown
	case a == EffectsKnown || b == EffectsKnown:
		return EffectsKnown
	default:
		return EffectsNone
	}
}

// ReadOperations normalizes every invocation of the session tree.
func (e *Evidence) ReadOperations(ctx context.Context, reads programReads) ([]Operation, error) {
	sources := operationSources(reads)
	var out []Operation
	for _, session := range e.Sessions {
		messages := callMessages(session.Messages)
		for _, receipt := range session.Invocations {
			var ops []Operation
			for _, source := range sources {
				found, err := source.Operations(ctx, receipt, session.ID, messages[receipt.ToolCallID])
				if err != nil {
					return nil, fmt.Errorf("receipt %s: %w", receipt.ID, err)
				}
				if found != nil {
					ops = found
					break
				}
			}
			out = append(out, ops...)
		}
	}
	return out, nil
}

// callMessages maps each tool call to the assistant message that made it.
func callMessages(messages []api.Message) map[string]string {
	out := map[string]string{}
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			out[call.ID] = message.ID
		}
	}
	return out
}
