package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// ProgramExecutionBinding is frozen receipt provenance, never a current grant.
// The source pin is execution input and cannot establish model observation.
type ProgramExecutionBinding struct {
	ParentReceiptID  string
	ParentToolCallID string
	ParentTool       string
	LanguageVersion  int
	DefinitionDigest string
	Invocation       toolcontract.OperationInvocation
	Source           toolcontract.SourcePin
}

func (r *SQLRecorder) ReadProgramExecutionBinding(ctx context.Context, sessionID, executionID string) (ProgramExecutionBinding, error) {
	var out ProgramExecutionBinding
	if r == nil || r.database == nil {
		return out, fmt.Errorf("invocation recorder is not configured")
	}
	var scope string
	v := &out.Invocation
	v.Kind = toolcontract.ProgramChildInvocation
	err := r.database.QueryRowContext(ctx, `SELECT p.parent_receipt_id,r.tool_call_id,r.tool_name,p.language_version,e.definition_digest,e.program_id,e.node_id,e.id,e.parent_execution_id,e.actor_id,r.session_id,e.scope_json,n.source_unit_id,n.start_byte,n.end_byte,n.line,n.column_number,p.catalog_digest,s.source_id,s.revision,s.representation,s.sha256,s.start_byte,s.end_byte FROM invocation_program_executions e JOIN invocation_programs p ON p.id=e.program_id JOIN invocation_receipts r ON r.id=p.parent_receipt_id JOIN invocation_program_nodes n ON n.program_id=e.program_id AND n.node_id=e.node_id JOIN invocation_program_sources s ON s.program_id=n.program_id AND s.source_unit_id=n.source_unit_id WHERE e.id=? AND r.session_id=?`, executionID, sessionID).Scan(&out.ParentReceiptID, &out.ParentToolCallID, &out.ParentTool, &out.LanguageVersion, &out.DefinitionDigest, &v.Identity.ProgramID, &v.Identity.NodeID, &v.Identity.ExecutionID, &v.Identity.ParentExecutionID, &v.ActorID, &v.SessionID, &scope, &v.SourceUnitID, &v.Span.StartByte, &v.Span.EndByte, &v.Span.Line, &v.Span.Column, &v.CatalogDigest, &out.Source.SourceID, &out.Source.Revision, &out.Source.Representation, &out.Source.SHA256, &out.Source.StartByte, &out.Source.EndByte)
	if err != nil {
		return out, err
	}
	decoder := json.NewDecoder(strings.NewReader(scope))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&v.Scope); err != nil {
		return out, err
	}
	if err = v.Validate(); err != nil {
		return out, err
	}
	return out, nil
}
