package search

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

const gateShape = "gate"

// ProjectGateEvidenceInput carries attribution for a gate/inspector evidence record.
type ProjectGateEvidenceInput struct {
	ProjectID     string
	SessionID     string
	WorkflowRunID string
	Record        evidence.Record
}

// ProjectGateEvidence maps an evidence.Record with GateType set into evidence_index rows.
func ProjectGateEvidence(in ProjectGateEvidenceInput) []IndexRow {
	rec := in.Record
	projectID := strings.TrimSpace(in.ProjectID)
	gateType := strings.TrimSpace(rec.GateType)
	runID := strings.TrimSpace(firstNonEmpty(in.WorkflowRunID, rec.RunID))
	if projectID == "" || gateType == "" || runID == "" {
		return nil
	}
	slot := strings.TrimSpace(rec.Slot)
	suffix := gateType + "\x00" + slot + "\x00" + strings.TrimSpace(rec.RecordedAt) + "\x00" + strings.TrimSpace(rec.GateVerdict)
	ts := strings.TrimSpace(rec.RecordedAt)
	return []IndexRow{{
		ID:            RowID(SourceGateEvidence, runID, suffix),
		ProjectID:     projectID,
		Source:        SourceGateEvidence,
		HitKind:       HitKindOutcome,
		SessionID:     strings.TrimSpace(in.SessionID),
		SourceRef:     runID,
		WorkflowRunID: runID,
		Kind:          gateType,
		Shape:         gateShape,
		Snippet:       gateEvidenceSnippet(rec),
		Verdict:       strings.TrimSpace(rec.GateVerdict),
		TS:            ts,
	}}
}

func gateEvidenceSnippet(rec evidence.Record) string {
	parts := make([]string, 0, 4)
	if s := strings.TrimSpace(rec.Summary); s != "" {
		parts = append(parts, s)
	}
	if rec.Artifacts != nil {
		for _, key := range []string{
			"threat_model", "claims", "vulnerabilities", "hardening", "accepted_residuals",
			"dismissed", "coverage_gaps", "winner",
		} {
			if v, ok := rec.Artifacts[key].(string); ok {
				if v = strings.TrimSpace(v); v != "" {
					parts = append(parts, fmt.Sprintf("%s: %s", key, v))
				}
			}
		}
	}
	return strings.Join(parts, " · ")
}
