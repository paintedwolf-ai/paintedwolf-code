package toolexecution

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// ProgramFeedbackLedger supplies immutable receipt provenance and actual effect
// observations. This projection grants no operation or source-read authority.
type ProgramFeedbackLedger interface {
	ReadProgramFeedback(context.Context, string, string, string, int, invocation.ProgramFeedbackAdmission) (*invocation.ProgramFeedbackLease, error)
}

// ProgramDiagnosticScreen is the host's bounded capture screening of owner
// diagnostic data before model-visible feedback publication.
type ProgramDiagnosticScreen interface {
	ProjectOperationDiagnostic(context.Context, toolcontract.ReviewStateAdmission, map[string]any) (map[string]any, func(), error)
}

// PreparedProgramFailure keeps supplemental feedback charged until the caller
// has serialized/delivered it and no longer retains the returned rejection data.
type PreparedProgramFailure struct {
	Rejection     *toolrejection.ToolReject
	diagnostic    toolcontract.NativeValue
	operation     toolcontract.BoundOperation
	progress      bool
	identity      tools.InvocationIdentity
	memory        ProgramStateAdmission
	policy        *toolfeedback.ProgramPolicyBlock
	release       func()
	mu            sync.Mutex
	closed        bool
	rendered      bool
	renderedError error
}

func (p *PreparedProgramFailure) Close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		p.Rejection = nil
		p.diagnostic = toolcontract.NativeValue{}
		p.operation = toolcontract.BoundOperation{}
		p.progress = false
		p.identity = tools.InvocationIdentity{}
		p.memory = nil
		p.policy = nil
		p.renderedError = nil
		if p.release != nil {
			p.release()
			p.release = nil
		}
	}
}

// Diagnostic is the owner's exact diagnostic data as a host-only reversible
// inspection value. The model-visible Rejection carries only screened data.
func (p *PreparedProgramFailure) Diagnostic() (toolcontract.NativeValue, error) {
	if p == nil {
		return toolcontract.NativeValue{}, fmt.Errorf("prepared program feedback required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || !p.diagnostic.Valid() {
		return toolcontract.NativeValue{}, fmt.Errorf("program feedback diagnostic is closed")
	}
	return p.diagnostic, nil
}

// PrepareProgramFailure supplements an actual native rejection. External output
// and ordinary utility failures are never converted into host policy codes.
// The introducing command entry calls this after the existing owner settles.
func PrepareProgramFailure(ctx context.Context, ledger ProgramFeedbackLedger, memory ProgramStateAdmission, screen ProgramDiagnosticScreen, tc tools.ToolContext, operation toolcontract.BoundOperation, cause error) (prepared *PreparedProgramFailure, err error) {
	rejected := toolrejection.AsToolReject(cause)
	if rejected == nil {
		return nil, nil
	}
	inv := operation.Invocation()
	if ctx == nil || ledger == nil || memory == nil || screen == nil || tc.Identity.ToolCallID == "" || tc.Identity.SessionID != inv.SessionID {
		return nil, fmt.Errorf("program feedback requires actual parent, ledger, diagnostic screening and memory admission")
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
	diagnostic, screened, releaseDiag, err := prepareFeedbackDiagnostic(ctx, memory, screen, rejected.Data)
	if err != nil {
		return nil, err
	}
	defer func() {
		if prepared == nil {
			releaseDiag()
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
	execution := retained.Execution
	switch execution.State {
	case "completed", "refused", "failed", "interrupted":
	default:
		return nil, fmt.Errorf("program feedback execution has not settled")
	}
	if execution.ID != inv.Identity.ExecutionID || (rejected.Code != execution.StopCode && rejected.Code != execution.HostStopCode) {
		return nil, fmt.Errorf("program feedback rejection does not match settled execution")
	}
	projection := retained.Projection
	if err = validateProgramFeedbackBinding(tc, operation, retained.Binding, projection); err != nil {
		return nil, err
	}
	facts, err := programOutcomeFacts(projection)
	if err != nil {
		return nil, err
	}
	clone := *rejected
	clone.Data = make(map[string]any, len(screened)+len(facts)+16)
	for name, value := range screened {
		clone.Data[name] = value
	}
	completeNativeRejection(operation, &clone)
	for name, value := range facts {
		clone.Data[name] = value
	}
	clone.Data["tool"] = "command"
	clone.Data["model_tool_call_id"] = tc.Identity.ToolCallID
	clone.Data["native_operation"] = operation.Definition().Facts().Identity
	clone.Data["native_command"] = operation.Command()
	clone.Data["execution_id"] = inv.Identity.ExecutionID
	clone.Data["node_id"] = inv.Identity.NodeID
	if _, present := clone.Data["source_unit_id"]; !present {
		clone.Data["source_unit_id"] = inv.SourceUnitID
	}
	clone.Data["operation_source_unit_id"] = inv.SourceUnitID
	clone.Data["operation_source_start_byte"], clone.Data["operation_source_end_byte"] = inv.Span.StartByte, inv.Span.EndByte
	clone.Data["operation_source_line"], clone.Data["operation_source_column"] = inv.Span.Line, inv.Span.Column
	var policy *toolfeedback.ProgramPolicyBlock
	errors.As(cause, &policy)
	if policy != nil && !policy.MatchesOperation(operation) {
		return nil, fmt.Errorf("program policy copy belongs to another execution")
	}
	return &PreparedProgramFailure{policy: policy, Rejection: &clone, diagnostic: diagnostic, operation: operation, identity: tc.Identity, memory: memory, release: func() { retained.Close(); releaseDiag() }}, nil
}

// PrepareProgramProgressFailure supplements a progress-envelope refusal from
// PrepareProgramEnvelope or ApplyProgramProgress after receipt settlement.
func PrepareProgramProgressFailure(ctx context.Context, ledger ProgramFeedbackLedger, memory ProgramStateAdmission, screen ProgramDiagnosticScreen, frame ProgramFrame, tc tools.ToolContext, cause error) (prepared *PreparedProgramFailure, err error) {
	rejected := toolrejection.AsToolReject(cause)
	if rejected == nil {
		return nil, nil
	}
	if ctx == nil || ledger == nil || memory == nil || screen == nil || tc.Identity.ToolCallID == "" {
		return nil, fmt.Errorf("program progress feedback requires frame, ledger, diagnostic screening and memory admission")
	}
	end, err := frame.beginUse()
	if err != nil {
		return nil, err
	}
	defer end()
	if err = frame.validateHostContext(ctx, tc); err != nil {
		return nil, err
	}
	parent := frame.lifetime.facts.parent
	for _, identity := range []string{tc.Identity.ToolCallID, parent.Identity.ProgramID, parent.Identity.NodeID, parent.Identity.ExecutionID, parent.SourceUnitID} {
		if len(identity) > maxProgramFeedbackReferenceBytes {
			return nil, fmt.Errorf("program feedback identity exceeds projection ceiling")
		}
	}
	diagnostic, screened, releaseDiag, err := prepareFeedbackDiagnostic(ctx, memory, screen, rejected.Data)
	if err != nil {
		return nil, err
	}
	defer func() {
		if prepared == nil {
			releaseDiag()
		}
	}()
	retained, err := ledger.ReadProgramFeedback(ctx, parent.SessionID, parent.Identity.ProgramID, "", 2, memory)
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
	if projection.ParentTool != "command" || projection.ParentToolCallID != tc.Identity.ToolCallID || projection.ParentSessionID != parent.SessionID || projection.Program.ID != parent.Identity.ProgramID || projection.Program.CatalogDigest != parent.CatalogDigest || projection.Program.State != "rejected" || projection.Program.ProgressState != "rejected" || projection.Program.StopCode != rejected.Code {
		return nil, fmt.Errorf("program progress feedback does not match settled envelope")
	}
	facts, err := programOutcomeFacts(projection)
	if err != nil {
		return nil, err
	}
	clone := *rejected
	clone.Data = make(map[string]any, len(screened)+len(facts)+12)
	for name, value := range screened {
		clone.Data[name] = value
	}
	for name, value := range facts {
		clone.Data[name] = value
	}
	clone.Data["tool"] = "command"
	clone.Data["model_tool_call_id"] = tc.Identity.ToolCallID
	clone.Data["native_operation"] = "update_progress"
	clone.Data["envelope_field"] = "progress"
	if projection.Progress != nil && projection.Progress.ExecutionID != "" {
		clone.Data["execution_id"] = projection.Progress.ExecutionID
	}
	if _, present := clone.Data["source_unit_id"]; !present {
		clone.Data["source_unit_id"] = parent.SourceUnitID
	}
	var policy *toolfeedback.ProgramPolicyBlock
	errors.As(cause, &policy)
	if policy != nil && !policy.MatchesProgress() {
		return nil, fmt.Errorf("program policy copy belongs to another execution")
	}
	return &PreparedProgramFailure{progress: true, policy: policy, Rejection: &clone, diagnostic: diagnostic, identity: tc.Identity, memory: memory, release: func() { retained.Close(); releaseDiag() }}, nil
}

func prepareFeedbackDiagnostic(ctx context.Context, memory ProgramStateAdmission, screen ProgramDiagnosticScreen, ownerData map[string]any) (toolcontract.NativeValue, map[string]any, func(), error) {
	if ownerData == nil {
		ownerData = map[string]any{}
	}
	diagnostic, releaseDiagnostic, err := toolcontract.NewNativeValue(ctx, memory, toolcontract.DiagnosticInspectionDomain, ownerData)
	if err != nil {
		return toolcontract.NativeValue{}, nil, nil, err
	}
	screened, releaseScreened, err := screen.ProjectOperationDiagnostic(ctx, memory, ownerData)
	if err != nil {
		releaseDiagnostic()
		return toolcontract.NativeValue{}, nil, nil, err
	}
	if screened == nil || releaseScreened == nil {
		releaseDiagnostic()
		return toolcontract.NativeValue{}, nil, nil, fmt.Errorf("diagnostic screening returned no retained projection")
	}
	release, err := memory.ReserveProgramState(ctx, 128<<10)
	if err != nil {
		releaseScreened()
		releaseDiagnostic()
		return toolcontract.NativeValue{}, nil, nil, err
	}
	if release == nil {
		releaseScreened()
		releaseDiagnostic()
		return toolcontract.NativeValue{}, nil, nil, fmt.Errorf("program feedback admission returned no lease")
	}
	return diagnostic, screened, func() { release(); releaseScreened(); releaseDiagnostic() }, nil
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
