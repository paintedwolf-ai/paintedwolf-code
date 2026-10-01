package inspector

import (
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
)

// GateVerdicts that satisfy a gate when anchors are present.
var passVerdicts = map[evidence.GateVerdict]bool{
	evidence.GateVerdictPassed:   true,
	evidence.GateVerdictApproved: true,
}

// AdvisoryTypes never satisfy dispatch or closeout gates alone.
var advisoryTypes = map[evidence.GateType]bool{
	evidence.GateTypePlanReview:    true,
	evidence.GateTypePlanReviewAlt: true,
}

// EvidenceAnchored reports whether record verdict and artifacts satisfy gate policy.
func EvidenceAnchored(rec evidence.Record) (bool, string) {
	if advisoryTypes[rec.TypedGateType()] {
		return false, "advisory evidence type"
	}
	if !passVerdicts[rec.TypedGateVerdict()] {
		return false, "verdict not passing"
	}
	switch rec.TypedGateType() {
	case evidence.GateTypeVerify, evidence.GateTypeTest:
		if rec.Artifacts == nil {
			return false, "missing artifacts"
		}
		if _, ok := rec.Artifacts["exit_code"]; !ok {
			return false, "missing exit_code anchor"
		}
		if cmd, _ := rec.Artifacts["command"].(string); strings.TrimSpace(cmd) == "" {
			return false, "missing command anchor"
		}
	case evidence.GateTypeSecurity:
		if rec.Artifacts == nil {
			return false, "missing artifacts"
		}
		if snapshotID, _ := rec.Artifacts["source_snapshot_id"].(string); strings.TrimSpace(snapshotID) == "" {
			return false, "missing source_snapshot_id anchor"
		}
		if _, ok := rec.Artifacts["sarif_statistics"]; !ok {
			return false, "missing sarif_statistics anchor"
		}
		coverage, _ := rec.Artifacts["coverage_status"].(string)
		if strings.TrimSpace(coverage) == "" {
			return false, "missing coverage_status anchor"
		}
		if coverage != "complete" {
			return false, "security scan coverage is " + coverage
		}
		if fingerprint, _ := rec.Artifacts["execution_fingerprint"].(string); strings.TrimSpace(fingerprint) == "" {
			return false, "missing execution_fingerprint anchor"
		}
		if scheme, _ := rec.Artifacts["fingerprint_scheme"].(string); strings.TrimSpace(scheme) == "" {
			return false, "missing fingerprint_scheme anchor"
		}
	case evidence.GateTypeSurveyClaims, evidence.GateTypeSurveyChallenged, evidence.GateTypeOptionsJudge:
		if !hasNonEmptyCitedEvidence(rec.Artifacts) {
			return false, "missing cited_evidence anchor"
		}
	case evidence.GateTypeReview, evidence.GateTypePlanReview, evidence.GateTypePlanReviewAlt:
	}
	return true, ""
}

func hasNonEmptyCitedEvidence(artifacts map[string]any) bool {
	if artifacts == nil {
		return false
	}
	raw, ok := artifacts[evidence.CitedEvidenceArtifactKey]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case []any:
		return len(v) > 0
	case []map[string]any:
		return len(v) > 0
	default:
		return false
	}
}

// SecurityEvidenceMatchesSnapshot validates the evidence source identity.
func SecurityEvidenceMatchesSnapshot(rec evidence.Record, sourceSnapshotID string) (bool, string) {
	ok, reason := EvidenceAnchored(rec)
	if !ok {
		return false, reason
	}
	if rec.TypedGateType() != evidence.GateTypeSecurity {
		return false, "not security evidence"
	}
	got, _ := rec.Artifacts["source_snapshot_id"].(string)
	if strings.TrimSpace(sourceSnapshotID) != "" && strings.TrimSpace(got) != strings.TrimSpace(sourceSnapshotID) {
		return false, "source snapshot stale"
	}
	return true, ""
}

// ExitCodePassed reports whether artifacts indicate a successful command run.
func ExitCodePassed(artifacts map[string]any) bool {
	if artifacts == nil {
		return false
	}
	switch v := artifacts["exit_code"].(type) {
	case float64:
		return int(v) == 0
	case int:
		return v == 0
	case int64:
		return v == 0
	default:
		return false
	}
}
