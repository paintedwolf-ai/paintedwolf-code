package inspector

import (
	"github.com/lycaon/lycaon/internal/evidence"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestEvidenceAnchoredMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		rec    evidence.Record
		wantOK bool
	}{
		{
			name: "verify passed without artifacts",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeVerify),
				GateVerdict: string(evidence.GateVerdictPassed),
			},
			wantOK: false,
		},
		{
			name: "verify passed missing command",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeVerify),
				GateVerdict: string(evidence.GateVerdictPassed),
				Artifacts: map[string]any{
					"exit_code": 0,
				},
			},
			wantOK: false,
		},
		{
			name: "verify passed anchored",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeVerify),
				GateVerdict: string(evidence.GateVerdictPassed),
				Artifacts: map[string]any{
					"exit_code": 0,
					"command":   "go test ./...",
				},
			},
			wantOK: true,
		},
		{
			name: "test passed anchored",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeTest),
				GateVerdict: string(evidence.GateVerdictPassed),
				Artifacts: map[string]any{
					"exit_code": 0,
					"command":   "npm test",
				},
			},
			wantOK: true,
		},
		{
			name: "security passed without source_snapshot_id",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeSecurity),
				GateVerdict: string(evidence.GateVerdictPassed),
				Artifacts: map[string]any{
					"sarif_statistics": map[string]any{"critical": 0},
				},
			},
			wantOK: false,
		},
		{
			name: "security passed without sarif_statistics",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeSecurity),
				GateVerdict: string(evidence.GateVerdictPassed),
				Artifacts: map[string]any{
					"source_snapshot_id": "snapshot-1",
				},
			},
			wantOK: false,
		},
		{
			name: "security passed anchored",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeSecurity),
				GateVerdict: string(evidence.GateVerdictPassed),
				Artifacts: map[string]any{
					"source_snapshot_id":    "snapshot-1",
					"sarif_statistics":      map[string]any{"critical": 0},
					"coverage_status":       "complete",
					"execution_fingerprint": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					"fingerprint_scheme":    api.ScanFingerprintScheme,
				},
			},
			wantOK: true,
		},
		{
			name: "security partial coverage cannot anchor",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeSecurity),
				GateVerdict: string(evidence.GateVerdictPassed),
				Artifacts: map[string]any{
					"source_snapshot_id":    "snapshot-1",
					"sarif_statistics":      map[string]any{"critical": 0},
					"coverage_status":       "partial",
					"execution_fingerprint": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
					"fingerprint_scheme":    api.ScanFingerprintScheme,
				},
			},
			wantOK: false,
		},
		{
			name: "plan_review advisory",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypePlanReview),
				GateVerdict: string(evidence.GateVerdictApproved),
			},
			wantOK: false,
		},
		{
			name: "plan_review_alt advisory",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypePlanReviewAlt),
				GateVerdict: string(evidence.GateVerdictApproved),
			},
			wantOK: false,
		},
		{
			name: "failed verdict",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeVerify),
				GateVerdict: string(evidence.GateVerdictFailed),
				Artifacts: map[string]any{
					"exit_code": 1,
					"command":   "go test ./...",
				},
			},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ok, reason := EvidenceAnchored(tc.rec)
			if ok != tc.wantOK {
				t.Fatalf("EvidenceAnchored() = %v reason=%q want %v", ok, reason, tc.wantOK)
			}
		})
	}
}

func TestEvidenceAnchoredReviewFamily(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		rec    evidence.Record
		wantOK bool
	}{
		{
			name: "survey_claims missing cited_evidence",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeSurveyClaims),
				GateVerdict: string(evidence.GateVerdictApproved),
				Artifacts: map[string]any{
					"claims": "1. SQLi",
				},
			},
			wantOK: false,
		},
		{
			name: "survey_claims anchored",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeSurveyClaims),
				GateVerdict: string(evidence.GateVerdictApproved),
				Artifacts: map[string]any{
					"claims": "1. SQLi",
					"cited_evidence": []any{
						map[string]any{"path": "auth.go", "line": 1, "excerpt": "query"},
					},
				},
			},
			wantOK: true,
		},
		{
			name: "survey_challenged anchored",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeSurveyChallenged),
				GateVerdict: string(evidence.GateVerdictApproved),
				Artifacts: map[string]any{
					"cited_evidence": []any{
						map[string]any{"path": "auth.go", "line": 1, "excerpt": "query"},
					},
				},
			},
			wantOK: true,
		},
		{
			name: "options_judge anchored",
			rec: evidence.Record{
				GateType:    string(evidence.GateTypeOptionsJudge),
				GateVerdict: string(evidence.GateVerdictApproved),
				Artifacts: map[string]any{
					"winner": "B",
					"cited_evidence": []any{
						map[string]any{"path": "plan.md", "line": 2, "excerpt": "option B"},
					},
				},
			},
			wantOK: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ok, reason := EvidenceAnchored(tc.rec)
			if ok != tc.wantOK {
				t.Fatalf("EvidenceAnchored() = %v reason=%q want %v", ok, reason, tc.wantOK)
			}
		})
	}
}
