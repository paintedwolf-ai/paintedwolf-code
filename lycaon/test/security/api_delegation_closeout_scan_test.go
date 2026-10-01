package security

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDelegationCloseoutWithoutSecurityEvidenceGatePending(t *testing.T) {
	stageEmptyHintsAndSuppressions(t)
	projectDir := t.TempDir()
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)

	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)

	store, obligation := commitSecurityLanding(t, sqlDB, projectDir, "dep-closeout")
	checker := &scan.SecurityCloseoutChecker{
		Store:    store,
		Evidence: evidenceStore,
	}

	depStore := delegation.NewMemoryStore()
	if _, err := depStore.Create(context.Background(), api.Delegation{
		ID:        "dep-closeout",
		ProjectID: testdbseed.DefaultProjectID, WorkspacePath: projectDir,
		Phase: api.DelegationPhaseWorker,
	}, "sess-closeout", []api.Leg{{ID: "leg-1", Status: api.LegStatusComplete}}); err != nil {
		t.Fatal(err)
	}

	gate := &delegation.InspectorCloseoutGate{
		Inspector: inspector.NewSimpleInspector(evidenceStore),
		Store:     depStore,
		Security:  checker,
	}
	err := gate.Check(context.Background(), "dep-closeout")
	if _, ok := scan.AsGatePending(err); !ok {
		t.Fatalf("expected gate_pending without evidence, got %v", err)
	}

	completeSecurityLanding(t, store, evidenceStore, projectDir, obligation)

	if err := gate.Check(context.Background(), "dep-closeout"); err != nil {
		t.Fatalf("expected closeout pass after ingested evidence: %v", err)
	}

	// MCP meta.guidance alone does not satisfy gate evidence (no JSONL anchors).
	metaOnly := evidence.Record{
		GateType:    string(evidence.GateTypeSecurity),
		GateVerdict: string(evidence.GateVerdictPassed),
		Artifacts: map[string]any{
			"guidance": []api.ScanGuidanceSummary{{Code: "SCAN_SQL_INJECTION", Message: "from mcp"}},
		},
	}
	ok, _ := inspector.EvidenceAnchored(metaOnly)
	if ok {
		t.Fatal("MCP guidance-only record must not satisfy security gate anchors")
	}
}
