package inspector_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
)

func TestLatestEvidenceScopedByLeg(t *testing.T) {
	records := []evidence.Record{
		{
			GateType: string(evidence.GateTypeVerify),
			Artifacts: map[string]any{
				inspector.ArtifactLegID: "leg-a",
				"exit_code":             0,
				"command":               "go test ./...",
			},
		},
		{
			GateType: string(evidence.GateTypeVerify),
			Artifacts: map[string]any{
				inspector.ArtifactLegID: "leg-b",
				"exit_code":             0,
				"command":               "go test ./...",
			},
		},
	}
	scopeA := inspector.EvidenceScope{LegID: "leg-a"}
	if rec := inspector.LatestScoped(records, scopeA); rec == nil || rec.Artifacts[inspector.ArtifactLegID] != "leg-a" {
		t.Fatalf("leg-a scope = %+v", rec)
	}
	scopeB := inspector.EvidenceScope{LegID: "leg-b"}
	if rec := inspector.LatestScoped(records, scopeB); rec == nil || rec.Artifacts[inspector.ArtifactLegID] != "leg-b" {
		t.Fatalf("leg-b scope = %+v", rec)
	}
	if rec := inspector.LatestScoped(records, inspector.EvidenceScope{LegID: "leg-missing"}); rec != nil {
		t.Fatalf("missing leg should be nil, got %+v", rec)
	}
}
