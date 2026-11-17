package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
)

// Extends the evidence anchor contract with closeout gate requirements.
func TestEvidenceAnchorsSecurityGateRequiresSnapshotAndSARIF(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		artifacts map[string]any
		wantOK    bool
	}{
		{
			name: "missing source_snapshot_id",
			artifacts: map[string]any{
				"sarif_statistics": map[string]any{"total": 0},
			},
		},
		{
			name: "missing sarif_statistics",
			artifacts: map[string]any{
				"source_snapshot_id": "snapshot-1",
			},
		},
		{
			name: "anchored passing",
			artifacts: map[string]any{
				"source_snapshot_id":    "snapshot-1",
				"sarif_statistics":      map[string]any{"total": 0},
				"coverage_status":       "complete",
				"execution_fingerprint": "fingerprint-1",
				"fingerprint_scheme":    "security-finding-v1",
			},
			wantOK: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ok, _ := inspector.EvidenceAnchored(evidence.Record{
				GateType:    string(evidence.GateTypeSecurity),
				GateVerdict: string(evidence.GateVerdictPassed),
				Artifacts:   tc.artifacts,
			})
			if ok != tc.wantOK {
				t.Fatalf("anchored = %v want %v", ok, tc.wantOK)
			}
		})
	}
}

func TestSecurityEvidenceRejectsStaleSnapshot(t *testing.T) {
	t.Parallel()
	rec := evidence.Record{
		GateType:    string(evidence.GateTypeSecurity),
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"source_snapshot_id":    "old-snapshot",
			"sarif_statistics":      map[string]any{"total": 0},
			"coverage_status":       "complete",
			"execution_fingerprint": "fingerprint-1",
			"fingerprint_scheme":    "security-finding-v1",
		},
	}
	ok, reason := inspector.SecurityEvidenceMatchesSnapshot(rec, "new-snapshot")
	if ok || reason == "" {
		t.Fatalf("expected stale rejection, ok=%v reason=%q", ok, reason)
	}
}
