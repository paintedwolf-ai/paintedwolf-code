//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/delegation"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestInspectorCloseoutGateWaitsForLandedScanThenPasses(t *testing.T) {
	projectDir := t.TempDir()
	evidenceStore := inspector.NewJSONLStore(inspector.DefaultEvidenceDir)
	ins := inspector.NewSimpleInspector(evidenceStore)
	ins.ProjectDir = func(context.Context, string) (string, error) { return projectDir, nil }

	depStore := delegation.NewMemoryStore()
	if _, err := depStore.Create(t.Context(), api.Delegation{
		ID: "dep-close", ProjectID: testdbseed.DefaultProjectID, WorkspacePath: projectDir,
		Phase: api.DelegationPhaseWorker,
	}, "sess-close", []api.Leg{{ID: "leg-1"}}); err != nil {
		t.Fatal(err)
	}

	sqlDB := testdbfixture.Open(t, "store.db")
	store, obligation := commitRequiredLanding(t, sqlDB, projectDir, "dep-close")
	gate := &delegation.InspectorCloseoutGate{
		Inspector: ins,
		Store:     depStore,
		Security:  &scan.SecurityCloseoutChecker{Store: store, Evidence: evidenceStore},
	}

	err := gate.Check(t.Context(), "dep-close")
	if pending, ok := scan.AsGatePending(err); !ok || pending.ScanID != obligation.ID {
		t.Fatalf("expected obligation pending, got %v", err)
	}
	completeSecurityEvidence(t, store, evidenceStore, projectDir, obligation, &scanoutput.Result{})
	if err := gate.Check(t.Context(), "dep-close"); err != nil {
		t.Fatalf("expected anchored obligation to pass: %v", err)
	}
}
