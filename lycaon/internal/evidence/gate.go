package evidence

import "time"

// Reserved artifact keys on a gate record: the citation channels a verdict
// carries beside its schema members. One spelling, since the stamping,
// anchoring, and reporting sides all read them.
const (
	CitedEvidenceArtifactKey = "cited_evidence"
	CitedURLsArtifactKey     = "cited_urls"
)

// GateRecord builds an inspector gate row in the unified Record shape.
func GateRecord(
	gateType GateType,
	slot, runID string,
	verdict GateVerdict,
	summary string,
	artifacts map[string]any,
	inspectorAgent, model, headSHA string,
	attempt int,
	at time.Time,
) Record {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return Record{
		GateType:       string(gateType),
		Slot:           slot,
		RunID:          runID,
		GateVerdict:    string(verdict),
		Summary:        summary,
		Artifacts:      artifacts,
		InspectorAgent: inspectorAgent,
		GateModel:      model,
		Attempt:        attempt,
		HeadSHA:        headSHA,
		RecordedAt:     at.UTC().Format(time.RFC3339Nano),
	}
}

// TypedGateType returns the gate type for a gate row.
func (r Record) TypedGateType() GateType {
	return GateType(r.GateType)
}

// TypedGateVerdict returns the gate verdict for a gate row.
func (r Record) TypedGateVerdict() GateVerdict {
	return GateVerdict(r.GateVerdict)
}

// GateAt parses RecordedAt for gate rows.
func (r Record) GateAt() time.Time {
	if r.RecordedAt == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, r.RecordedAt)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, r.RecordedAt)
	}
	return t
}
