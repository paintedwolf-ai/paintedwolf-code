package cmddiag

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func cleanWEAObservation() WEAObservation {
	return WEAObservation{
		SessionID:    "11111111-2222-3333-4444-555555555555",
		WorkerID:     "worker-alpha",
		JobID:        "job-wea-1",
		ResultHandle: "res-handle-1",
		InvocationID: "inv-wea-1",
		Settled:      true,
		WorkerResults: []WEAWorkerResultFact{
			{
				ResultHandle: "res-handle-1",
				WorkerID:     "worker-alpha",
				JobID:        "job-wea-1",
				SessionID:    "11111111-2222-3333-4444-555555555555",
				Settled:      true,
				ChangedPaths: []string{"internal/app/service.go"},
				SourceRef:    "worker_results/res-handle-1.json",
			},
		},
		LandedPaths: []WEALandedPathFact{
			{
				ResultHandle:  "res-handle-1",
				WorkerID:      "worker-alpha",
				JobID:         "job-wea-1",
				Path:          "internal/app/service.go",
				DiskChecked:   true,
				PresentOnDisk: true,
				Settled:       true,
				SourceRef:     "promotions/res-handle-1.json",
			},
		},
		WorkerCards: []WEAWorkerCardFact{
			{
				ResultHandle:      "res-handle-1",
				CardDelivered:     true,
				CardRevision:      4,
				CommittedRevision: 4,
			},
		},
		ReadBases: []WEAReadBasisFact{
			{
				ReadID:       "read-1",
				SessionID:    "11111111-2222-3333-4444-555555555555",
				InvocationID: "inv-wea-1",
				Path:         "internal/app/service.go",
				Revision:     "rev-10",
				Qualifying:   true,
				Active:       true,
				SourceRef:    "reads/read-1.json",
			},
		},
		EditEffects: []WEAEditEffectFact{
			{
				EffectID:          "eff-edit-1",
				VersionID:         "ver-11",
				SessionID:         "11111111-2222-3333-4444-555555555555",
				WorkerID:          "worker-alpha",
				InvocationID:      "inv-wea-1",
				Path:              "internal/app/service.go",
				Accepted:          true,
				ReadBasisRevision: "rev-10",
				CurrentRevision:   "rev-10",
				AuthorID:          "worker-alpha",
				AuthorKind:        "worker",
				SourceRef:         "effects/eff-edit-1.json",
			},
		},
		FileVersions: []WEAFileVersionFact{
			{
				VersionID:      "ver-11",
				EffectID:       "eff-edit-1",
				SessionID:      "11111111-2222-3333-4444-555555555555",
				WorkerID:       "worker-alpha",
				Path:           "internal/app/service.go",
				AuthorID:       "worker-alpha",
				AuthorKind:     "worker",
				BaseRevision:   "rev-10",
				ResultRevision: "rev-11",
				SourceRef:      "versions/ver-11.json",
			},
		},
		ApprovalPlans: []WEAApprovalPlanFact{
			{
				ApprovalPlanID: "plan-1",
				SessionID:      "11111111-2222-3333-4444-555555555555",
				InvocationID:   "inv-wea-1",
				ActionKind:     "command",
				TargetRef:      "workspace/repo",
				ActionDigest:   "sha256:plan1",
				Approved:       true,
				SourceRef:      "approvals/plan-1.json",
			},
		},
		Grants: []WEAGrantFact{
			{
				GrantID:      "grant-chat-1",
				SessionID:    "11111111-2222-3333-4444-555555555555",
				Scope:        "chat",
				ActionKind:   "egress",
				TargetRef:    "https://fixture.invalid/api",
				ActionDigest: "sha256:grant1",
				Active:       true,
				SourceRef:    "grants/grant-chat-1.json",
			},
		},
		AuthorityEffects: []WEAAuthorityEffectFact{
			{
				EffectID:          "eff-auth-1",
				SessionID:         "11111111-2222-3333-4444-555555555555",
				WorkerID:          "worker-alpha",
				InvocationID:      "inv-wea-1",
				ActionKind:        "command",
				TargetRef:         "workspace/repo",
				ActionDigest:      "sha256:plan1",
				ApprovalPlanID:    "plan-1",
				RequiresAuthority: true,
				Executed:          true,
				SourceRef:         "effects/eff-auth-1.json",
			},
		},
		EgressDecisions: []WEAEgressDecisionFact{
			{
				DecisionID:      "dec-egress-1",
				SessionID:       "11111111-2222-3333-4444-555555555555",
				WorkerID:        "worker-alpha",
				InvocationID:    "inv-wea-1",
				DestinationRef:  "https://fixture.invalid/api",
				Admitted:        true,
				DecisionOrdinal: 10,
				SourceRef:       "egress/dec-egress-1.json",
			},
		},
		ObservedConnections: []WEAEgressConnectionFact{
			{
				ConnectionID:    "conn-1",
				SessionID:       "11111111-2222-3333-4444-555555555555",
				WorkerID:        "worker-alpha",
				InvocationID:    "inv-wea-1",
				DestinationRef:  "https://fixture.invalid/api",
				ObservedOrdinal: 11,
				Settled:         true,
				SourceRef:       "fixture_endpoint/conn-1.json",
			},
		},
		SecretCanaries: []WEASecretCanaryFact{
			{
				CanaryID:     "canary-1",
				SessionID:    "11111111-2222-3333-4444-555555555555",
				Scope:        "run",
				CanaryDigest: "ref:canary1digest",
				CanaryValue:  "pw_synth_canary_9f8e7d6c5b4a3210",
			},
		},
		RecordedArtifacts: []WEARecordedArtifactFact{
			{
				RecordID:              "rec-1",
				SessionID:             "11111111-2222-3333-4444-555555555555",
				WorkerID:              "worker-alpha",
				JobID:                 "job-wea-1",
				ResultHandle:          "res-handle-1",
				RecordKind:            "worker_result",
				ScreenedReferenceOnly: true,
				ContentBytes:          "uses secret reference sec_ref_abc123 only",
				SourceRef:             "records/rec-1.json",
			},
		},
	}
}

func marshalFindings(t *testing.T, findings []Finding) string {
	t.Helper()
	b, err := json.Marshal(findings)
	if err != nil {
		t.Fatalf("marshal findings: %v", err)
	}
	return string(b)
}

func TestEvaluateWEAQuietOnCleanRun(t *testing.T) {
	fixed := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	findings := EvaluateWEA(cleanWEAObservation(), fixed)
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings on clean WEA run, got %+v", findings)
	}
}

func TestEvaluateWEAContradictionFixtures(t *testing.T) {
	fixed := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	t.Run("1a_landed_path_absent_from_worker_result", func(t *testing.T) {
		obs := cleanWEAObservation()
		const secretPath = "/Users/dev/private_repo_canary/internal/unlisted.go"
		obs.LandedPaths = append(obs.LandedPaths, WEALandedPathFact{
			ResultHandle:  "res-handle-1",
			WorkerID:      "worker-alpha",
			JobID:         "job-wea-1",
			Path:          secretPath,
			DiskChecked:   true,
			PresentOnDisk: true,
			Settled:       true,
			SourceRef:     "/Users/dev/private_repo_canary/promotions/p1.json",
		})

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEALandedPathMismatch || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "landed_path_absent_from_worker_result" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
		if len(f.SourceIDs) == 0 {
			t.Fatalf("expected redacted source locators, got %+v", f)
		}
		raw := marshalFindings(t, findings)
		for _, forbidden := range []string{"private_repo_canary", "unlisted.go", secretPath} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("finding leaked raw path %q: %s", forbidden, raw)
			}
		}
	})

	t.Run("1b_claimed_landed_path_missing_on_disk", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.LandedPaths[0].DiskChecked = true
		obs.LandedPaths[0].PresentOnDisk = false

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEALandedPathMismatch || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "landed_path_missing_on_disk" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
	})

	t.Run("2a_edit_accepted_against_stale_read_basis", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.EditEffects[0].Path = "/Users/dev/secret_workspace/pkg/stale_file.go"
		obs.ReadBases[0].Path = obs.EditEffects[0].Path
		obs.EditEffects[0].ReadBasisRevision = "rev-08"
		obs.EditEffects[0].CurrentRevision = "rev-10"
		obs.EditEffects[0].RebasedCleanly = false

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEAEditBasisOrAuthorMismatch || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "accepted_edit_stale_read_basis" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
		raw := marshalFindings(t, findings)
		for _, forbidden := range []string{"secret_workspace", "stale_file.go"} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("finding leaked raw path %q: %s", forbidden, raw)
			}
		}
	})

	t.Run("2b_edit_wrong_recorded_author", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.FileVersions[0].AuthorID = "person-spoofed"
		obs.FileVersions[0].AuthorKind = "person"

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEAEditBasisOrAuthorMismatch || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "version_author_disagrees_with_effect" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
	})

	t.Run("3a_effect_executed_without_matching_approval_or_grant", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.AuthorityEffects = append(obs.AuthorityEffects, WEAAuthorityEffectFact{
			EffectID:          "eff-unauth-1",
			SessionID:         obs.SessionID,
			InvocationID:      "inv-unauth-1",
			ActionKind:        "command",
			TargetRef:         "/Users/dev/secret_target/token=sk-1234567890abcdef1234567890",
			ActionDigest:      "sha256:unauth",
			RequiresAuthority: true,
			Executed:          true,
			SourceRef:         "effects/eff-unauth-1.json",
		})

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEAUnauthorizedEffect || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "effect_executed_without_matching_approval_or_grant" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
		raw := marshalFindings(t, findings)
		for _, forbidden := range []string{"secret_target", "sk-1234567890abcdef"} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("finding leaked sensitive target %q: %s", forbidden, raw)
			}
		}
	})

	t.Run("3b_chat_grant_applied_across_chats", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.Grants = append(obs.Grants, WEAGrantFact{
			GrantID:      "grant-other-chat",
			SessionID:    "99999999-aaaa-bbbb-cccc-dddddddddddd",
			Scope:        "chat",
			ActionKind:   "command",
			ActionDigest: "sha256:crosschat",
			Active:       true,
			SourceRef:    "grants/grant-other-chat.json",
		})
		obs.AuthorityEffects = append(obs.AuthorityEffects, WEAAuthorityEffectFact{
			EffectID:          "eff-cross-chat-1",
			SessionID:         obs.SessionID,
			InvocationID:      "inv-cross-1",
			ActionKind:        "command",
			ActionDigest:      "sha256:crosschat",
			GrantID:           "grant-other-chat",
			RequiresAuthority: true,
			Executed:          true,
			SourceRef:         "effects/eff-cross-chat-1.json",
		})

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEAUnauthorizedEffect || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "chat_grant_applied_across_sessions" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
	})

	t.Run("4a_egress_connection_unrecorded_by_product", func(t *testing.T) {
		obs := cleanWEAObservation()
		const rawURL = "https://exfil.fixture.invalid/leak?token=Bearer%20sk-ant-api03-verysecretvalue1234567890"
		obs.ObservedConnections = append(obs.ObservedConnections, WEAEgressConnectionFact{
			ConnectionID:    "conn-unrecorded-1",
			SessionID:       obs.SessionID,
			InvocationID:    "inv-egress-unrec",
			DestinationRef:  rawURL,
			ObservedOrdinal: 20,
			Settled:         true,
			SourceRef:       "fixture_endpoint/conn-unrecorded-1.json",
		})

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEAUnrecordedOrRefusedEgress || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "egress_connection_unrecorded_by_product" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
		raw := marshalFindings(t, findings)
		for _, forbidden := range []string{"exfil.fixture.invalid", "sk-ant-api03", "verysecretvalue"} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("finding leaked raw destination %q: %s", forbidden, raw)
			}
		}
	})

	t.Run("4b_egress_connection_arriving_after_refusal", func(t *testing.T) {
		obs := cleanWEAObservation()
		const deniedURL = "https://denied.fixture.invalid/post"
		obs.EgressDecisions = append(obs.EgressDecisions, WEAEgressDecisionFact{
			DecisionID:      "dec-refused-1",
			SessionID:       obs.SessionID,
			InvocationID:    "inv-egress-refused",
			DestinationRef:  deniedURL,
			Admitted:        false,
			Refused:         true,
			RefusalCode:     "WEB_SEARCH_HOST_DENIED",
			DecisionOrdinal: 30,
			SourceRef:       "egress/dec-refused-1.json",
		})
		obs.ObservedConnections = append(obs.ObservedConnections, WEAEgressConnectionFact{
			ConnectionID:    "conn-after-refusal-1",
			SessionID:       obs.SessionID,
			InvocationID:    "inv-egress-refused",
			DestinationRef:  deniedURL,
			ObservedOrdinal: 31,
			Settled:         true,
			SourceRef:       "fixture_endpoint/conn-after-refusal-1.json",
		})

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEAUnrecordedOrRefusedEgress || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "egress_connection_after_refusal" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
		raw := marshalFindings(t, findings)
		if strings.Contains(raw, "denied.fixture.invalid") {
			t.Fatalf("finding leaked denied destination: %s", raw)
		}
	})

	t.Run("5_managed_secret_canary_in_persisted_record", func(t *testing.T) {
		obs := cleanWEAObservation()
		const canary = "pw_synth_canary_9f8e7d6c5b4a3210"
		const payload = "tool_output: authorization header leaked " + canary + " end_payload_marker"
		obs.RecordedArtifacts = append(obs.RecordedArtifacts, WEARecordedArtifactFact{
			RecordID:     "rec-leak-1",
			SessionID:    obs.SessionID,
			WorkerID:     "worker-alpha",
			JobID:        "job-wea-1",
			ResultHandle: "res-handle-1",
			EffectID:     "eff-leak-1",
			InvocationID: "inv-leak-1",
			RecordKind:   "tool_result",
			ContentBytes: payload,
			SourceRef:    "/Users/dev/profile/records/rec-leak-1.json",
		})

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEASecretCanaryLeak || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "managed_secret_canary_in_record" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
		if len(f.ExecutionIDs) != 1 || f.ExecutionIDs[0] != "rec-leak-1" {
			t.Fatalf("expected record_id locator in ExecutionIDs, got %+v", f.ExecutionIDs)
		}
		if len(f.OperationIDs) != 1 || f.OperationIDs[0] != "canary-1" {
			t.Fatalf("expected canary_id locator in OperationIDs, got %+v", f.OperationIDs)
		}
		if len(f.SourceIDs) == 0 {
			t.Fatalf("expected redacted source locators, got %+v", f)
		}
		raw := marshalFindings(t, findings)
		for _, forbidden := range []string{canary, "end_payload_marker", "authorization header leaked", "/Users/dev/profile"} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("finding leaked raw secret or payload %q: %s", forbidden, raw)
			}
		}
	})
}

func TestEvaluateWEAHealthyControlsProduceZeroContradictions(t *testing.T) {
	fixed := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	t.Run("healthy_lag", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.Settled = false
		obs.WorkerResults[0].Settled = false
		obs.WorkerResults[0].ChangedPaths = nil
		obs.LandedPaths[0].Settled = false
		obs.EgressDecisions = nil
		obs.ObservedConnections[0].Settled = false

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 0 {
			t.Fatalf("healthy_lag must produce 0 findings, got %+v", findings)
		}
	})

	t.Run("late_card", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.WorkerCards = []WEAWorkerCardFact{
			{
				ResultHandle:      "res-handle-1",
				CardDelivered:     false,
				CardRevision:      1,
				CommittedRevision: 4,
			},
		}

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 0 {
			t.Fatalf("late_card must produce 0 findings, got %+v", findings)
		}
	})

	t.Run("expected_refusal", func(t *testing.T) {
		obs := cleanWEAObservation()
		// Refused stale edit
		obs.EditEffects = append(obs.EditEffects, WEAEditEffectFact{
			EffectID:          "eff-edit-refused",
			SessionID:         obs.SessionID,
			Path:              "internal/app/service.go",
			Accepted:          false,
			Refused:           true,
			RefusalCode:       "EDIT_STALE_READ_BASIS",
			ReadBasisRevision: "rev-01",
			CurrentRevision:   "rev-10",
		})
		// Refused unauthorized effect
		obs.AuthorityEffects = append(obs.AuthorityEffects, WEAAuthorityEffectFact{
			EffectID:          "eff-auth-refused",
			SessionID:         obs.SessionID,
			ActionKind:        "command",
			RequiresAuthority: true,
			Executed:          false,
			Refused:           true,
			RefusalCode:       "APPROVAL_DENIED",
		})
		// Refused egress with no observed connection
		obs.EgressDecisions = append(obs.EgressDecisions, WEAEgressDecisionFact{
			DecisionID:      "dec-egress-refused",
			SessionID:       obs.SessionID,
			InvocationID:    "inv-refused-egress",
			DestinationRef:  "https://blocked.fixture.invalid/nope",
			Admitted:        false,
			Refused:         true,
			RefusalCode:     "WEB_SEARCH_HOST_DENIED",
			DecisionOrdinal: 40,
		})

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 0 {
			t.Fatalf("expected_refusal must produce 0 findings, got %+v", findings)
		}
	})

	t.Run("retention_gap", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.RetentionGapped = true
		obs.WorkerResults = nil
		obs.ReadBases = nil
		obs.ApprovalPlans = nil
		obs.Grants = nil
		obs.EgressDecisions = nil

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 0 {
			t.Fatalf("retention_gap must produce 0 findings, got %+v", findings)
		}

		// Also verify per-fact retention pruning when global RetentionGapped is false.
		perFact := cleanWEAObservation()
		perFact.WorkerResults = nil
		perFact.LandedPaths[0].RetentionPruned = true
		perFact.ReadBases[0].RetentionPruned = true
		perFact.ApprovalPlans = nil
		perFact.AuthorityEffects[0].RetentionPruned = true
		perFact.EgressDecisions = nil
		perFact.ObservedConnections[0].RetentionPruned = true

		findings = EvaluateWEA(perFact, fixed)
		if len(findings) != 0 {
			t.Fatalf("per-fact retention_gap must produce 0 findings, got %+v", findings)
		}
	})

	t.Run("clean_rebase", func(t *testing.T) {
		obs := cleanWEAObservation()
		// CurrentRevision moved from rev-10 to rev-12 due to non-overlapping edit,
		// but this edit rebased cleanly from its qualifying rev-10 read basis.
		obs.EditEffects[0].ReadBasisRevision = "rev-10"
		obs.EditEffects[0].CurrentRevision = "rev-12"
		obs.EditEffects[0].RebasedCleanly = true
		obs.EditEffects[0].RebasedFromRevision = "rev-10"

		findings := EvaluateWEA(obs, fixed)
		if len(findings) != 0 {
			t.Fatalf("clean_rebase must produce 0 findings, got %+v", findings)
		}
	})
}

func TestWEAObserverDisabledIsZeroAllocNoOp(t *testing.T) {
	obs := NewWEAObserver(WEAConfig{Enabled: false})
	if obs.Enabled() {
		t.Fatal("expected WEAObserver to be disabled")
	}

	sample := cleanWEAObservation()
	sample.LandedPaths[0].PresentOnDisk = false

	var fetchCalls atomic.Int64
	task := WEATask{
		SessionID: sample.SessionID,
		ScopeKey:  sample.ResultHandle,
		Fetch: func(context.Context) (WEAObservation, error) {
			fetchCalls.Add(1)
			return sample, nil
		},
	}

	if obs.Submit(sample) {
		t.Fatal("disabled Submit must return false")
	}
	if obs.SubmitTask(task) {
		t.Fatal("disabled SubmitTask must return false")
	}
	if fetchCalls.Load() != 0 {
		t.Fatalf("disabled SubmitTask invoked Fetch %d times", fetchCalls.Load())
	}

	fixed := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	if got := EvaluateWEAIfEnabled(false, sample, fixed); got != nil {
		t.Fatalf("EvaluateWEAIfEnabled(false, ...) = %+v, want nil", got)
	}

	// Verify O(1) zero-allocation no-op when disabled.
	submitAllocs := testing.AllocsPerRun(100, func() {
		_ = obs.Submit(sample)
	})
	if submitAllocs != 0 {
		t.Fatalf("disabled Submit allocated %v allocs/op, want 0", submitAllocs)
	}
	evalAllocs := testing.AllocsPerRun(100, func() {
		_ = EvaluateWEAIfEnabled(false, sample, fixed)
	})
	if evalAllocs != 0 {
		t.Fatalf("disabled EvaluateWEAIfEnabled allocated %v allocs/op, want 0", evalAllocs)
	}

	obs.Close()
	rep := obs.Report()
	if len(rep.Findings) != 0 || rep.Stats != (Stats{}) {
		t.Fatalf("disabled observer emitted report: %+v", rep)
	}
}

func TestWEAObserverBoundedDeduplicatedReport(t *testing.T) {
	fixed := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var sinkFindings []Finding

	obs := NewWEAObserver(WEAConfig{
		Enabled:       true,
		QueueCapacity: 16,
		Now:           func() time.Time { return fixed },
		Sink: func(f Finding) {
			mu.Lock()
			sinkFindings = append(sinkFindings, f)
			mu.Unlock()
		},
	})

	bad := cleanWEAObservation()
	bad.LandedPaths[0].PresentOnDisk = false

	if !obs.Submit(bad) || !obs.Submit(bad) || !obs.Submit(bad) {
		t.Fatal("expected submissions to enqueue")
	}
	obs.Close()

	rep := obs.Report()
	if len(rep.Findings) != 1 {
		t.Fatalf("expected 1 deduplicated finding in report, got %d: %+v", len(rep.Findings), rep.Findings)
	}
	if rep.Findings[0].RuleID != RuleWEALandedPathMismatch || rep.Findings[0].Disposition != DispositionContradiction {
		t.Fatalf("unexpected report finding: %+v", rep.Findings[0])
	}
	if rep.Stats.Emitted != 1 || rep.Stats.Deduplicated != 2 {
		t.Fatalf("unexpected stats: %+v", rep.Stats)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sinkFindings) != 1 {
		t.Fatalf("expected 1 sink finding, got %d", len(sinkFindings))
	}
}
