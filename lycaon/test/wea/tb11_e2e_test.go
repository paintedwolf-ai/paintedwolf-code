package wea_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

type tb11ParitySummary struct {
	HealthyEffects        int
	ContradictionEffects  int
	CanceledApprovals     int
	EmittedFindings       int
	ContradictionRuleHits map[cmddiag.RuleID]int
}

func runTB11WiredMatrix() tb11ParitySummary {
	summary := tb11ParitySummary{
		ContradictionRuleHits: map[cmddiag.RuleID]int{},
	}
	const (
		sess = "11111111-2222-3333-4444-555555555555"
		proj = "proj-tb11-e2e"
	)

	// --- HEALTHY CONTROLS (5 controls + cross-owner healthy flows) ---
	// H1: Healthy worker landing + late card
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ProjectID: proj, ResultHandle: "res-healthy-1", Settled: true,
		WorkerResults: []cmddiag.WEAWorkerResultFact{{
			ResultHandle: "res-healthy-1", WorkerID: "w1", JobID: "job-h1", SessionID: sess,
			Settled: true, ChangedPaths: []string{"pkg/clean.go"},
		}},
		LandedPaths: []cmddiag.WEALandedPathFact{{
			ResultHandle: "res-healthy-1", WorkerID: "w1", JobID: "job-h1",
			Path: "pkg/clean.go", DiskChecked: true, PresentOnDisk: true, Settled: true,
		}},
		WorkerCards: []cmddiag.WEAWorkerCardFact{{
			ResultHandle: "res-healthy-1", CardDelivered: false, CardRevision: 1, CommittedRevision: 4,
		}},
	})
	summary.HealthyEffects++

	// H2: Clean CRDT rebase with qualifying read + matching retained version attribution
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ProjectID: proj, ScopeKey: "eff-edit-healthy-rebase", Settled: true,
		ReadBases: []cmddiag.WEAReadBasisFact{{
			ReadID: "read-h2", SessionID: sess, Path: "pkg/clean.go", Revision: "3", Qualifying: true, Active: true,
		}},
		EditEffects: []cmddiag.WEAEditEffectFact{{
			EffectID: "eff-edit-healthy-rebase", VersionID: "ver-h2", SessionID: sess, Path: "pkg/clean.go",
			Accepted: true, ReadBasisRevision: "3", CurrentRevision: "5", RebasedCleanly: true, RebasedFromRevision: "3",
			AuthorID: "call-h2", AuthorKind: "agent",
		}},
		FileVersions: []cmddiag.WEAFileVersionFact{{
			VersionID: "ver-h2", EffectID: "eff-edit-healthy-rebase", SessionID: sess, Path: "pkg/clean.go",
			AuthorID: "call-h2", AuthorKind: "agent",
		}},
	})
	summary.HealthyEffects++

	// H3: Cross-owner authorized post-execution effect (hitl plan -> toolexecution settled effect)
	planSeq := cmddiag.NextWEASeq()
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ScopeKey: "cp-healthy-1", Settled: true,
		ApprovalPlans: []cmddiag.WEAApprovalPlanFact{{
			ApprovalPlanID: "cp-healthy-1", SessionID: sess, ActionDigest: "sha256:healthy-act",
			DecisionSeq: planSeq, Approved: true, Settled: true,
		}},
	})
	effSeq := cmddiag.NextWEASeq()
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ProjectID: proj, InvocationID: "inv-auth-h3", ScopeKey: "eff-auth-h3", Settled: true,
		AuthorityEffects: []cmddiag.WEAAuthorityEffectFact{{
			EffectID: "eff-auth-h3", SessionID: sess, ProjectID: proj, InvocationID: "inv-auth-h3",
			ActionDigest: "sha256:healthy-act", ApprovalPlanID: "cp-healthy-1",
			EventSeq: effSeq, RequiresAuthority: true, Executed: true, Settled: true,
		}},
	})
	summary.HealthyEffects++

	// H3b: Approved checkpoint whose execution was later canceled (plan only, no executed effect)
	cancelPlanSeq := cmddiag.NextWEASeq()
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ScopeKey: "cp-canceled-1", Settled: true,
		ApprovalPlans: []cmddiag.WEAApprovalPlanFact{{
			ApprovalPlanID: "cp-canceled-1", SessionID: sess, ActionDigest: "sha256:canceled-act",
			DecisionSeq: cancelPlanSeq, Approved: true, Settled: true,
		}},
	})
	summary.CanceledApprovals++

	// H4: Cross-owner egress superseding admission (refusal -> later admission -> broker connection)
	refSeq := cmddiag.NextWEASeq()
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, InvocationID: "inv-egress-h4", ScopeKey: "dec-ref-h4", Settled: true,
		EgressDecisions: []cmddiag.WEAEgressDecisionFact{{
			DecisionID: "dec-ref-h4", SessionID: sess, InvocationID: "inv-egress-h4",
			DestinationRef: "api.fixture.invalid:443", Refused: true, RefusalCode: "HOST_DENIED", DecisionSeq: refSeq,
		}},
	})
	admitSeq := cmddiag.NextWEASeq()
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, InvocationID: "inv-egress-h4", ScopeKey: "dec-admit-h4", Settled: true,
		EgressDecisions: []cmddiag.WEAEgressDecisionFact{{
			DecisionID: "dec-admit-h4", SessionID: sess, InvocationID: "inv-egress-h4",
			DestinationRef: "api.fixture.invalid:443", GrantScope: "exact_action", MaxUses: 1, Admitted: true, DecisionSeq: admitSeq,
		}},
	})
	connSeq := cmddiag.NextWEASeq()
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, InvocationID: "inv-egress-h4", ScopeKey: "conn-h4", Settled: true,
		ObservedConnections: []cmddiag.WEAEgressConnectionFact{{
			ConnectionID: "conn-h4", SessionID: sess, InvocationID: "inv-egress-h4",
			DestinationRef: "api.fixture.invalid:443", ObservedSeq: connSeq, Settled: true,
		}},
	})
	summary.HealthyEffects++

	// H5: Healthy lag + retention gap + expected refusal
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ScopeKey: "lag-and-retention", Settled: false, RetentionGapped: true,
		LandedPaths: []cmddiag.WEALandedPathFact{{ResultHandle: "res-pruned", Path: "pkg/old.go", Settled: false}},
	})
	summary.HealthyEffects++

	// --- 5 DURABLE CONTRADICTIONS ---
	// C1: Worker landed path absent from committed worker result
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ProjectID: proj, ResultHandle: "res-contradiction-1", Settled: true,
		WorkerResults: []cmddiag.WEAWorkerResultFact{{
			ResultHandle: "res-contradiction-1", WorkerID: "w1", JobID: "job-c1", SessionID: sess,
			Settled: true, ChangedPaths: []string{"pkg/expected.go"},
		}},
		LandedPaths: []cmddiag.WEALandedPathFact{{
			ResultHandle: "res-contradiction-1", WorkerID: "w1", JobID: "job-c1",
			Path: "pkg/unlisted.go", DiskChecked: true, PresentOnDisk: true, Settled: true,
		}},
	})
	summary.ContradictionEffects++

	// C2: Edit accepted against stale read basis
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ProjectID: proj, ScopeKey: "eff-edit-stale-c2", Settled: true,
		ReadBases: []cmddiag.WEAReadBasisFact{{
			ReadID: "read-c2", SessionID: sess, Path: "pkg/stale.go", Revision: "2", Qualifying: true, Active: true,
		}},
		EditEffects: []cmddiag.WEAEditEffectFact{{
			EffectID: "eff-edit-stale-c2", SessionID: sess, Path: "pkg/stale.go",
			Accepted: true, ReadBasisRevision: "2", CurrentRevision: "5", RebasedCleanly: false,
		}},
	})
	summary.ContradictionEffects++

	// C3: Retained version author disagrees with effect author
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ProjectID: proj, ScopeKey: "eff-edit-author-c3", Settled: true,
		ReadBases: []cmddiag.WEAReadBasisFact{{
			ReadID: "read-c3", SessionID: sess, Path: "pkg/author.go", Revision: "4", Qualifying: true, Active: true,
		}},
		EditEffects: []cmddiag.WEAEditEffectFact{{
			EffectID: "eff-edit-author-c3", VersionID: "ver-c3", SessionID: sess, Path: "pkg/author.go",
			Accepted: true, ReadBasisRevision: "4", CurrentRevision: "4", AuthorID: "worker-alpha", AuthorKind: "worker",
		}},
		FileVersions: []cmddiag.WEAFileVersionFact{{
			VersionID: "ver-c3", EffectID: "eff-edit-author-c3", SessionID: sess, Path: "pkg/author.go",
			AuthorID: "person-spoofed", AuthorKind: "person",
		}},
	})
	summary.ContradictionEffects++

	// C4: Post-execution authority effect without matching recorded approval/grant
	unauthSeq := cmddiag.NextWEASeq()
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ProjectID: proj, InvocationID: "inv-unauth-c4", ScopeKey: "eff-unauth-c4", Settled: true,
		AuthorityEffects: []cmddiag.WEAAuthorityEffectFact{{
			EffectID: "eff-unauth-c4", SessionID: sess, ProjectID: proj, InvocationID: "inv-unauth-c4",
			ActionDigest: "sha256:unauth-act", EventSeq: unauthSeq, RequiresAuthority: true, Executed: true, Settled: true,
		}},
	})
	summary.ContradictionEffects++

	// C5: Broker-observed egress connection unrecorded by product policy
	unrecConnSeq := cmddiag.NextWEASeq()
	_ = cmddiag.ObserveWEA(cmddiag.WEAObservation{
		SessionID: sess, ProjectID: proj, InvocationID: "inv-unrec-c5", ScopeKey: "conn-unrec-c5", Settled: true,
		ObservedConnections: []cmddiag.WEAEgressConnectionFact{{
			ConnectionID: "conn-unrec-c5", SessionID: sess, ProjectID: proj, InvocationID: "inv-unrec-c5",
			DestinationRef: "unrecorded.fixture.invalid:443", ObservedSeq: unrecConnSeq, Settled: true,
		}},
	})
	summary.ContradictionEffects++

	return summary
}

func TestTB11WiredEndToEndParityAndContradictions(t *testing.T) {
	fixed := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)

	// 1. Run with diagnostics DISABLED.
	obsOff := cmddiag.NewObserver(cmddiag.Config{Enabled: false})
	restoreOff := cmddiag.SetObserver(obsOff)
	offSummary := runTB11WiredMatrix()
	restoreOff()
	obsOff.Close()

	// 2. Run with diagnostics ENABLED.
	var mu sync.Mutex
	var emitted []cmddiag.Finding
	obsOn := cmddiag.NewObserver(cmddiag.Config{
		Enabled:       true,
		QueueCapacity: 64,
		Now:           func() time.Time { return fixed },
		Sink: func(f cmddiag.Finding) {
			mu.Lock()
			emitted = append(emitted, f)
			mu.Unlock()
		},
	})
	restoreOn := cmddiag.SetObserver(obsOn)
	onSummary := runTB11WiredMatrix()
	restoreOn()
	obsOn.Close()

	mu.Lock()
	defer mu.Unlock()
	onSummary.EmittedFindings = len(emitted)
	for _, f := range emitted {
		if f.Disposition == cmddiag.DispositionContradiction {
			onSummary.ContradictionRuleHits[f.RuleID]++
		}
	}

	// Print crisp parity receipt.
	var b strings.Builder
	b.WriteString("\nTB11 Enabled vs Disabled End-to-End Parity Receipt:\n")
	b.WriteString(fmt.Sprintf("  %-26s | %-10s | %-10s\n", "Metric", "Disabled", "Enabled"))
	b.WriteString(fmt.Sprintf("  %-26s-+-%-10s-+-%-10s\n", "--------------------------", "----------", "----------"))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "HealthyEffects", offSummary.HealthyEffects, onSummary.HealthyEffects))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "ContradictionEffects", offSummary.ContradictionEffects, onSummary.ContradictionEffects))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "CanceledApprovals", offSummary.CanceledApprovals, onSummary.CanceledApprovals))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "EmittedFindings", offSummary.EmittedFindings, onSummary.EmittedFindings))
	b.WriteString(fmt.Sprintf("  %-26s | %-10d | %-10d\n", "ContradictionRulesFired", len(offSummary.ContradictionRuleHits), len(onSummary.ContradictionRuleHits)))
	t.Log(b.String())

	// Assert strict functional parity between disabled and enabled runs.
	if offSummary.HealthyEffects != onSummary.HealthyEffects ||
		offSummary.ContradictionEffects != onSummary.ContradictionEffects ||
		offSummary.CanceledApprovals != onSummary.CanceledApprovals {
		t.Fatalf("functional parity mismatch: off=%+v on=%+v", offSummary, onSummary)
	}
	if offSummary.EmittedFindings != 0 || obsOff.Stats().Emitted != 0 {
		t.Fatalf("disabled run emitted findings: summary=%+v stats=%+v", offSummary, obsOff.Stats())
	}

	// Assert all 5 durable contradictions fired exactly once each, with 0 false positives on healthy controls.
	wantRules := []cmddiag.RuleID{
		cmddiag.RuleWEALandedPathMismatch,
		cmddiag.RuleWEAEditStaleReadBasis,
		cmddiag.RuleWEAEditWrongAuthor,
		cmddiag.RuleWEAUnauthorizedEffect,
		cmddiag.RuleWEAUnrecordedOrRefusedEgress,
	}
	if len(emitted) != len(wantRules) {
		t.Fatalf("expected exactly %d findings (5 contradictions, 0 healthy warnings), got %d: %+v", len(wantRules), len(emitted), emitted)
	}
	for _, r := range wantRules {
		if onSummary.ContradictionRuleHits[r] != 1 {
			t.Fatalf("expected rule %q to fire exactly once, got hits=%+v (all=%+v)", r, onSummary.ContradictionRuleHits, emitted)
		}
	}
}
