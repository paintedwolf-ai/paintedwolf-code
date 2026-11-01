package toolexecution

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/invocation"
)

// programEffectDetails keeps complete identities and owner observations. The
// compact provider projection may omit a row; the aggregate counts and durable
// detail reference still describe its existence.
func programEffectDetails(program string, rows []invocation.ExecutionProjection) ([]any, int, error) {
	if len(rows) > 2 {
		return nil, 0, fmt.Errorf("program feedback exceeds effect detail count")
	}
	out := make([]any, 0, len(rows))
	omitted := 0
	for _, row := range rows {
		r := row.Receipt
		if r.ID == "" || r.ProgramID != program || r.NodeID == "" || row.SourceUnitID == "" || row.Span.StartByte < 0 || row.Span.EndByte < row.Span.StartByte || row.Span.Line < 1 || row.Span.Column < 1 {
			return nil, 0, fmt.Errorf("program effect detail has invalid provenance")
		}
		values := map[string]any{
			"execution_id": r.ID, "node_id": r.NodeID, "source_unit_id": row.SourceUnitID,
			"parent_execution_id": r.ParentExecutionID, "source_start_byte": row.Span.StartByte,
			"source_end_byte": row.Span.EndByte, "source_line": row.Span.Line, "source_column": row.Span.Column,
			"command": r.Command, "state": r.State, "effect_certainty": r.EffectCertainty,
			"owner": r.Owner, "owner_invoked": r.OwnerInvoked, "complete": r.Complete,
			"publication_eligible": r.PublicationEligible, "output_publishable": r.OutputPublishable,
			"stop_code": r.StopCode, "host_stop_code": r.HostStopCode, "detail_ref": r.DetailRef,
			"evidence_kind": r.EvidenceKind, "evidence_ref": r.EvidenceRef,
			"owner_ref": r.OwnerRef, "recovery_ref": r.RecoveryRef,
			"accepted_revision": r.AcceptedRevision, "published_revision": r.PublishedRevision,
		}
		if r.StatusCode != nil {
			values["status_code"] = *r.StatusCode
		}
		valid := true
		for name, value := range values {
			if text, ok := value.(string); ok {
				if len(text) > maxProgramFeedbackReferenceBytes {
					valid = false
					break
				}
				values[name] = strings.Clone(text)
			}
		}
		if !valid {
			omitted++
			continue
		}
		out = append(out, values)
	}
	return out, omitted, nil
}
