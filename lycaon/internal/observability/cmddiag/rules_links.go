package cmddiag

import (
	"slices"
	"time"
)

func checkOperationSourceContradiction(obs Observation, now time.Time) (Finding, bool) {
	var execIDs, opIDs, srcIDs, mismatches []string
	var incompleteExecs, unsupportedExecs, unsupportedSrcs []string
	var unsupportedReason string
	for _, link := range obs.LinkedOperations {
		if !link.Completed {
			continue
		}
		if link.UnsupportedReason != "" {
			unsupportedExecs = append(unsupportedExecs, link.ExecutionID)
			if link.SourceID != "" {
				unsupportedSrcs = append(unsupportedSrcs, link.SourceID)
			}
			if unsupportedReason == "" {
				unsupportedReason = link.UnsupportedReason
			}
			continue
		}
		if !link.CompleteEvidence {
			incompleteExecs = append(incompleteExecs, link.ExecutionID)
			continue
		}
		if link.ExpectedOperationID != "" && link.OperationID != "" && link.ExpectedOperationID != link.OperationID {
			mismatches = append(mismatches, "operation_id_mismatch")
			execIDs = append(execIDs, link.ExecutionID)
			opIDs = append(opIDs, link.ExpectedOperationID, link.OperationID)
		}
		if link.ExpectedSourceID != "" && link.SourceID != "" && link.ExpectedSourceID != link.SourceID {
			mismatches = append(mismatches, "source_id_mismatch")
			execIDs = append(execIDs, link.ExecutionID)
			srcIDs = append(srcIDs, link.ExpectedSourceID, link.SourceID)
		}
		if link.ExpectedRevision != "" && link.ObservedRevision != "" && link.ExpectedRevision != link.ObservedRevision {
			mismatches = append(mismatches, "revision_mismatch")
			execIDs = append(execIDs, link.ExecutionID)
			if link.SourceID != "" {
				srcIDs = append(srcIDs, link.SourceID)
			}
		}
	}
	if len(mismatches) > 0 {
		slices.Sort(mismatches)
		mismatches = slices.Compact(mismatches)
		f := baseFinding(obs, now, RuleOperationSourceContradiction, DispositionContradiction)
		f.ExecutionIDs = execIDs
		f.OperationIDs = opIDs
		f.SourceIDs = srcIDs
		f.Expected = map[string]any{"linked_operation_source_consistent": true}
		f.Observed = map[string]any{"contradiction_count": len(execIDs), "mismatches": mismatches}
		f.Reason = mismatches[0]
		return f, true
	}
	if len(incompleteExecs) > 0 {
		f := baseFinding(obs, now, RuleOperationSourceContradiction, DispositionIncomplete)
		f.ExecutionIDs = incompleteExecs
		f.Expected = map[string]any{"complete_linked_evidence": true}
		f.Observed = map[string]any{"incomplete_links": len(incompleteExecs)}
		f.Reason = "incomplete_operation_source_evidence"
		return f, true
	}
	if len(unsupportedExecs) > 0 {
		f := baseFinding(obs, now, RuleOperationSourceContradiction, DispositionUnsupported)
		f.ExecutionIDs = unsupportedExecs
		f.SourceIDs = unsupportedSrcs
		f.Expected = map[string]any{"independent_owner_evidence": true}
		f.Observed = map[string]any{"unsupported_links": len(unsupportedExecs)}
		f.Reason = unsupportedReason
		return f, true
	}
	return Finding{}, false
}

func checkStopAdmissionAfterAck(obs Observation, now time.Time) (Finding, bool) {
	if obs.StopAck == nil {
		return Finding{}, false
	}
	ack := obs.StopAck
	if !ack.Qualified || !ack.AdmissionWitnessComparable || ack.AckOrdinal <= 0 {
		f := baseFinding(obs, now, RuleStopAdmissionAfterAck, DispositionUnsupported)
		f.Expected = map[string]any{
			"qualified_stop_ack":           true,
			"comparable_admission_witness": true,
		}
		f.Observed = map[string]any{
			"qualified_stop_ack":           ack.Qualified,
			"comparable_admission_witness": ack.AdmissionWitnessComparable,
			"ack_ordinal":                  ack.AckOrdinal,
		}
		f.Reason = "missing_qualified_stop_ack_or_comparable_admission_witness"
		return f, true
	}
	var postAckExecs []string
	var maxOrdinal int64
	for _, ex := range obs.Executions {
		if !ex.Admitted || ex.AdmissionOrdinal <= ack.AckOrdinal {
			continue
		}
		postAckExecs = append(postAckExecs, ex.ExecutionID)
		if ex.AdmissionOrdinal > maxOrdinal {
			maxOrdinal = ex.AdmissionOrdinal
		}
	}
	if len(postAckExecs) == 0 {
		return Finding{}, false
	}
	f := baseFinding(obs, now, RuleStopAdmissionAfterAck, DispositionContradiction)
	f.ExecutionIDs = postAckExecs
	f.Expected = map[string]any{"max_admission_ordinal": ack.AckOrdinal, "stop_ref": ack.StopRef}
	f.Observed = map[string]any{"post_ack_admission_count": len(postAckExecs), "max_observed_ordinal": maxOrdinal}
	f.Reason = "admission_observed_after_qualified_stop_ack"
	return f, true
}
