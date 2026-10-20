package toolexecution

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// ProgramFeedbackLedger supplies immutable receipt provenance and actual effect
// observations. This projection grants no operation or source-read authority.
type ProgramFeedbackLedger interface {
	ReadProgramFeedback(context.Context, string, string, string, int, invocation.ProgramFeedbackAdmission) (*invocation.ProgramFeedbackLease, error)
}

// PreparedProgramFailure keeps supplemental feedback charged until the caller
// has serialized/delivered it and no longer retains the returned rejection data.
type PreparedProgramFailure struct {
	Rejection *toolrejection.ToolReject
	release   func()
	once      sync.Once
}

func (p *PreparedProgramFailure) Close() {
	if p != nil && p.release != nil {
		p.once.Do(p.release)
	}
}

// PrepareProgramFailure supplements an actual native rejection. External output
// and ordinary utility failures are never converted into host policy codes.
// The introducing command entry calls this after the existing owner settles.
func PrepareProgramFailure(ctx context.Context, ledger ProgramFeedbackLedger, memory ProgramStateAdmission, tc tools.ToolContext, operation toolcontract.BoundOperation, cause error) (prepared *PreparedProgramFailure, err error) {
	rejected := toolrejection.AsToolReject(cause)
	if rejected == nil {
		return nil, nil
	}
	inv := operation.Invocation()
	if ctx == nil || ledger == nil || memory == nil || tc.Identity.ToolCallID == "" || tc.Identity.SessionID != inv.SessionID {
		return nil, fmt.Errorf("program feedback requires actual parent, ledger and memory admission")
	}
	if err = inv.Validate(); err != nil {
		return nil, err
	}
	if err = operation.Definition().ValidateNativeCommands(); err != nil {
		return nil, err
	}
	// The ledger returns at most two complete and two uncertain details plus one
	// stopped row. Retained bodies are referenced, never hydrated here.
	for _, identity := range []string{tc.Identity.ToolCallID, inv.Identity.ProgramID, inv.Identity.NodeID, inv.Identity.ExecutionID, inv.Identity.ParentExecutionID, inv.SourceUnitID} {
		if len(identity) > maxProgramFeedbackReferenceBytes {
			return nil, fmt.Errorf("program feedback identity exceeds projection ceiling")
		}
	}
	// This reservation covers our bounded supplemental facts and shallow owner
	// detail map. SQL metadata has its own measured reservation below.
	release, err := memory.ReserveProgramState(ctx, int64(128<<10)+int64(len(rejected.Data))*128)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("program feedback admission returned no lease")
	}
	defer func() {
		if prepared == nil {
			release()
		}
	}()
	retained, err := ledger.ReadProgramFeedback(ctx, inv.SessionID, inv.Identity.ProgramID, inv.Identity.ExecutionID, 2, memory)
	if err != nil {
		return nil, err
	}
	if retained == nil {
		return nil, fmt.Errorf("program feedback ledger returned no retained snapshot")
	}
	defer func() {
		if prepared == nil {
			retained.Close()
		}
	}()
	projection := retained.Projection
	if err = validateProgramFeedbackBinding(tc, operation, retained.Binding, projection); err != nil {
		return nil, err
	}
	facts, err := programOutcomeFacts(projection)
	if err != nil {
		return nil, err
	}
	clone := *rejected
	clone.Data = maps.Clone(rejected.Data)
	if clone.Data == nil {
		clone.Data = map[string]any{}
	}
	for name, value := range facts {
		clone.Data[name] = value
	}
	clone.Data["tool"] = "command"
	clone.Data["model_tool_call_id"] = tc.Identity.ToolCallID
	clone.Data["native_operation"] = operation.Definition().Facts().Identity
	clone.Data["native_command"] = operation.Command()
	clone.Data["execution_id"] = inv.Identity.ExecutionID
	clone.Data["node_id"] = inv.Identity.NodeID
	clone.Data["source_unit_id"] = inv.SourceUnitID
	clone.Data["source_start_byte"], clone.Data["source_end_byte"] = inv.Span.StartByte, inv.Span.EndByte
	clone.Data["source_line"], clone.Data["source_column"] = inv.Span.Line, inv.Span.Column
	return &PreparedProgramFailure{Rejection: &clone, release: func() { retained.Close(); release() }}, nil
}

func validateProgramFeedbackBinding(tc tools.ToolContext, operation toolcontract.BoundOperation, b invocation.ProgramExecutionBinding, p invocation.ProgramProjection) error {
	inv := operation.Invocation()
	if b.Invocation != inv || b.DefinitionDigest != operation.Definition().Digest() || b.ParentTool != "command" || b.ParentToolCallID != tc.Identity.ToolCallID || b.ParentReceiptID != p.Program.ParentReceiptID || b.LanguageVersion != p.Program.LanguageVersion || p.ParentTool != "command" || p.ParentToolCallID != tc.Identity.ToolCallID || p.ParentSessionID != inv.SessionID || p.Program.ID != inv.Identity.ProgramID || p.Program.CatalogDigest != inv.CatalogDigest {
		return fmt.Errorf("program feedback provenance does not match actual command child")
	}
	if b.Source.Representation != toolcontract.UTF8SourceBytes || b.Source.SourceID == "" || b.Source.Revision == "" || len(b.Source.SHA256) != 64 || b.Source.EndByte < b.Source.StartByte || int64(inv.Span.StartByte) < b.Source.StartByte || int64(inv.Span.EndByte) > b.Source.EndByte {
		return fmt.Errorf("program feedback source does not match captured syntax unit")
	}
	if inv.SourceUnitID == p.RootSourceUnitID && b.Source.SHA256 != p.Program.SourceDigest {
		return fmt.Errorf("program feedback root source digest mismatch")
	}
	return nil
}
