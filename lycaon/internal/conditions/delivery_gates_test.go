package conditions_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestDeliveryGatesDoNotRequireTests(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		registry, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
			DelegationCloseout: func(context.Context, string) (bool, error) { return delivered, nil },
			DeliveryReported:   func(context.Context, string, string, string) (bool, error) { return delivered, nil },
			SourceVerifyPassed: func(context.Context, string) (bool, error) { return false, nil },
		})
		testutil.FailErr(t, "create registry", err)
		state := conditions.EvalContext{Ctx: t.Context(), SessionID: "session"}
		ready, err := registry.Evaluate("delivery_gates_passed", state)
		testutil.FailErr(t, "evaluate delivery", err)
		if ready != delivered {
			t.Fatalf("delivery=%v want %v", ready, delivered)
		}
		passed, err := registry.Evaluate("closeout_gates_passed", state)
		testutil.FailErr(t, "evaluate required tests", err)
		if passed {
			t.Fatal("delivery waived explicitly required tests")
		}
	}
}

func TestDeliveryGatesRequireSessionLandingScans(t *testing.T) {
	for _, tc := range []struct {
		name                                                                   string
		missingSession, noScan, pending, evidence, stale, histEvidence, want   bool
	}{
		{name: "missing session blocks scan obligation", missingSession: true},
		{name: "session has no required scan", noScan: true, want: true},
		{name: "session scan pending", pending: true},
		{name: "completed scan lacks evidence"},
		{name: "evidence belongs to another snapshot", evidence: true, stale: true},
		{name: "session scan has anchored evidence", evidence: true, want: true},
		{name: "historical delegation evidence satisfies gate", evidence: true, histEvidence: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scan := &api.CodeScan{ID: "scan", Status: api.CodeScanStatusComplete, SourceSnapshotID: "current"}
			if tc.histEvidence {
				scan.DelegationID = "dep-hist"
			}
			if tc.pending {
				scan.Status = api.CodeScanStatusRunning
			}
			if tc.noScan {
				scan = nil
			}
			deps := scanDomainDeps(stubScanLedger{latest: scan}, nil)
			deps.DeliveryReported = func(context.Context, string, string, string) (bool, error) { return true, nil }
			if tc.evidence {
				snapshot := "current"
				if tc.stale {
					snapshot = "old"
				}
				runKey := "session"
				if tc.histEvidence {
					runKey = "dep-hist"
				}
				deps.Evidence = stubEvidence{runKey + "/scan:scan/security": &evidence.Record{
					GateType: string(evidence.GateTypeSecurity), GateVerdict: string(evidence.GateVerdictPassed),
					Artifacts: completeSecurityArtifacts(snapshot),
				}}
			}
			registry, err := conditions.NewDefaultRegistry(deps)
			testutil.FailErr(t, "create delivery registry", err)
			sessionID := "session"
			if tc.missingSession {
				sessionID = ""
			}
			state := conditions.EvalContext{Ctx: t.Context(), SessionID: sessionID, ProjectDir: "/project"}
			ready, err := registry.Evaluate("delivery_gates_passed", state)
			testutil.FailErr(t, "evaluate delivery scan obligation", err)
			if ready != tc.want {
				t.Fatalf("delivery ready=%v want %v", ready, tc.want)
			}
			if tc.missingSession {
				fresh, err := registry.Evaluate("security_evidence_fresh", state)
				testutil.FailErr(t, "evaluate explicit security evidence", err)
				if fresh {
					t.Fatal("missing session reported fresh security evidence")
				}
			}
		})
	}
}
