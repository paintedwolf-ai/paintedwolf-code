package cmddiag

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

func cleanWEAObservation() WEAObservation {
	return WEAObservation{
		SessionID:    "11111111-2222-3333-4444-555555555555",
		ProjectID:    "proj-wea-1",
		DeviceID:     "dev-mac-1",
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
				ProjectID:      "proj-wea-1",
				InvocationID:   "inv-wea-1",
				ActionKind:     "command",
				TargetRef:      "workspace/repo",
				ActionDigest:   "sha256:plan1",
				DecisionSeq:    5,
				Approved:       true,
				Settled:        true,
				SourceRef:      "approvals/plan-1.json",
			},
		},
		Grants: []WEAGrantFact{
			{
				GrantID:      "grant-chat-1",
				SessionID:    "11111111-2222-3333-4444-555555555555",
				ProjectID:    "proj-wea-1",
				DeviceID:     "dev-mac-1",
				Scope:        "chat",
				ActionKind:   "egress",
				TargetRef:    "https://fixture.invalid/api",
				ActionDigest: "sha256:grant1",
				GrantedSeq:   4,
				Active:       true,
				Settled:      true,
				SourceRef:    "grants/grant-chat-1.json",
			},
		},
		AuthorityEffects: []WEAAuthorityEffectFact{
			{
				EffectID:          "eff-auth-1",
				SessionID:         "11111111-2222-3333-4444-555555555555",
				ProjectID:         "proj-wea-1",
				DeviceID:          "dev-mac-1",
				WorkerID:          "worker-alpha",
				InvocationID:      "inv-wea-1",
				ActionKind:        "command",
				TargetRef:         "workspace/repo",
				ActionDigest:      "sha256:plan1",
				ApprovalPlanID:    "plan-1",
				EventSeq:          6,
				RequiresAuthority: true,
				Executed:          true,
				Settled:           true,
				SourceRef:         "effects/eff-auth-1.json",
			},
		},
		EgressDecisions: []WEAEgressDecisionFact{
			{
				DecisionID:     "dec-egress-1",
				SessionID:      "11111111-2222-3333-4444-555555555555",
				ProjectID:      "proj-wea-1",
				DeviceID:       "dev-mac-1",
				WorkerID:       "worker-alpha",
				InvocationID:   "inv-wea-1",
				DestinationRef: "https://fixture.invalid/api",
				GrantScope:     "exact_action",
				MaxUses:        1,
				Admitted:       true,
				DecisionSeq:    10,
				SourceRef:      "egress/dec-egress-1.json",
			},
		},
		ObservedConnections: []WEAEgressConnectionFact{
			{
				ConnectionID:   "conn-1",
				SessionID:      "11111111-2222-3333-4444-555555555555",
				ProjectID:      "proj-wea-1",
				DeviceID:       "dev-mac-1",
				WorkerID:       "worker-alpha",
				InvocationID:   "inv-wea-1",
				DestinationRef: "https://fixture.invalid/api",
				ObservedSeq:    11,
				Settled:        true,
				SourceRef:      "fixture_endpoint/conn-1.json",
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
	if findings := EvaluateWEA(cleanWEAObservation(), fixed); len(findings) != 0 {
		t.Fatalf("expected 0 findings on clean WEA run, got %+v", findings)
	}
	if findings := Evaluate(WrapWEAObservation(cleanWEAObservation()), fixed); len(findings) != 0 {
		t.Fatalf("expected 0 findings through Evaluate(WrapWEAObservation), got %+v", findings)
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

		findings := Evaluate(WrapWEAObservation(obs), fixed)
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

		findings := Evaluate(WrapWEAObservation(obs), fixed)
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

	t.Run("2_edit_stale_read_basis_independent_rule", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.EditEffects[0].Path = "/Users/dev/secret_workspace/pkg/stale_file.go"
		obs.ReadBases[0].Path = obs.EditEffects[0].Path
		obs.EditEffects[0].ReadBasisRevision = "rev-08"
		obs.EditEffects[0].CurrentRevision = "rev-10"
		obs.EditEffects[0].RebasedCleanly = false

		findings := Evaluate(WrapWEAObservation(obs), fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEAEditStaleReadBasis || f.Disposition != DispositionContradiction {
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

	t.Run("3_edit_wrong_author_independent_rule", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.FileVersions[0].AuthorID = "person-spoofed"
		obs.FileVersions[0].AuthorKind = "person"

		findings := Evaluate(WrapWEAObservation(obs), fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWEAEditWrongAuthor || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "version_author_disagrees_with_effect" {
			t.Fatalf("unexpected reason %q", f.Reason)
		}
	})

	t.Run("2_and_3_both_edit_rules_emit_independently", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.EditEffects[0].ReadBasisRevision = "rev-08"
		obs.EditEffects[0].CurrentRevision = "rev-10"
		obs.FileVersions[0].AuthorID = "person-spoofed"

		findings := Evaluate(WrapWEAObservation(obs), fixed)
		if len(findings) != 2 {
			t.Fatalf("expected 2 independent findings (read-basis + author), got %+v", findings)
		}
		seen := map[RuleID]bool{}
		for _, f := range findings {
			seen[f.RuleID] = true
			if f.Disposition != DispositionContradiction {
				t.Fatalf("expected contradiction, got %+v", f)
			}
		}
		if !seen[RuleWEAEditStaleReadBasis] || !seen[RuleWEAEditWrongAuthor] {
			t.Fatalf("expected both %q and %q, got %+v", RuleWEAEditStaleReadBasis, RuleWEAEditWrongAuthor, findings)
		}
	})

	t.Run("4a_effect_executed_without_matching_approval_or_grant", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.AuthorityEffects = append(obs.AuthorityEffects, WEAAuthorityEffectFact{
			EffectID:          "eff-unauth-1",
			SessionID:         obs.SessionID,
			ProjectID:         obs.ProjectID,
			DeviceID:          obs.DeviceID,
			InvocationID:      "inv-unauth-1",
			ActionKind:        "command",
			TargetRef:         "/Users/dev/secret_target/token=sk-1234567890abcdef1234567890",
			ActionDigest:      "sha256:unauth",
			EventSeq:          12,
			RequiresAuthority: true,
			Executed:          true,
			Settled:           true,
			SourceRef:         "effects/eff-unauth-1.json",
		})

		findings := Evaluate(WrapWEAObservation(obs), fixed)
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

	t.Run("4b_all_grant_scopes_validated_strictly_no_wildcards", func(t *testing.T) {
		// Valid grants for exact_action, chat, project, device all pass.
		for _, tc := range []struct {
			name  string
			grant WEAGrantFact
		}{
			{
				name: "valid_exact_action",
				grant: WEAGrantFact{
					GrantID: "g-exact", SessionID: "11111111-2222-3333-4444-555555555555",
					InvocationID: "inv-sc-1", Scope: "exact_action", ActionDigest: "sha256:sc1",
					GrantedSeq: 8, Active: true, Settled: true,
				},
			},
			{
				name: "valid_chat",
				grant: WEAGrantFact{
					GrantID: "g-chat", SessionID: "11111111-2222-3333-4444-555555555555",
					Scope: "chat", ActionDigest: "sha256:sc1",
					GrantedSeq: 8, Active: true, Settled: true,
				},
			},
			{
				name: "valid_project",
				grant: WEAGrantFact{
					GrantID: "g-proj", ProjectID: "proj-wea-1",
					Scope: "project", ActionDigest: "sha256:sc1",
					GrantedSeq: 8, Active: true, Settled: true,
				},
			},
			{
				name: "valid_device",
				grant: WEAGrantFact{
					GrantID: "g-dev", DeviceID: "dev-mac-1",
					Scope: "device", ActionDigest: "sha256:sc1",
					GrantedSeq: 8, Active: true, Settled: true,
				},
			},
		} {
			obs := cleanWEAObservation()
			obs.Grants = append(obs.Grants, tc.grant)
			obs.AuthorityEffects = append(obs.AuthorityEffects, WEAAuthorityEffectFact{
				EffectID: "eff-sc-1", SessionID: obs.SessionID, ProjectID: obs.ProjectID,
				DeviceID: obs.DeviceID, InvocationID: "inv-sc-1", ActionDigest: "sha256:sc1",
				GrantID: tc.grant.GrantID, EventSeq: 9, RequiresAuthority: true, Executed: true, Settled: true,
			})
			if got := EvaluateWEA(obs, fixed); len(got) != 0 {
				t.Fatalf("%s: expected 0 findings, got %+v", tc.name, got)
			}
		}

		// Mismatched scope identities for all 4 scopes produce contradictions.
		for _, tc := range []struct {
			name       string
			grant      WEAGrantFact
			wantReason string
		}{
			{
				name: "exact_action_wrong_invocation",
				grant: WEAGrantFact{
					GrantID: "g-exact-bad", SessionID: "11111111-2222-3333-4444-555555555555",
					InvocationID: "inv-other", Scope: "exact_action", ActionDigest: "sha256:sc1",
					GrantedSeq: 8, Active: true, Settled: true,
				},
				wantReason: "exact_action_grant_scope_mismatch",
			},
			{
				name: "chat_wrong_session",
				grant: WEAGrantFact{
					GrantID: "g-chat-bad", SessionID: "99999999-aaaa-bbbb-cccc-dddddddddddd",
					Scope: "chat", ActionDigest: "sha256:sc1",
					GrantedSeq: 8, Active: true, Settled: true,
				},
				wantReason: "chat_grant_applied_across_sessions",
			},
			{
				name: "project_wrong_project",
				grant: WEAGrantFact{
					GrantID: "g-proj-bad", ProjectID: "proj-other",
					Scope: "project", ActionDigest: "sha256:sc1",
					GrantedSeq: 8, Active: true, Settled: true,
				},
				wantReason: "project_grant_applied_across_projects",
			},
			{
				name: "device_wrong_device",
				grant: WEAGrantFact{
					GrantID: "g-dev-bad", DeviceID: "dev-other",
					Scope: "device", ActionDigest: "sha256:sc1",
					GrantedSeq: 8, Active: true, Settled: true,
				},
				wantReason: "device_grant_applied_across_devices",
			},
		} {
			obs := cleanWEAObservation()
			obs.Grants = append(obs.Grants, tc.grant)
			obs.AuthorityEffects = append(obs.AuthorityEffects, WEAAuthorityEffectFact{
				EffectID: "eff-sc-bad", SessionID: obs.SessionID, ProjectID: obs.ProjectID,
				DeviceID: obs.DeviceID, InvocationID: "inv-sc-1", ActionDigest: "sha256:sc1",
				GrantID: tc.grant.GrantID, EventSeq: 9, RequiresAuthority: true, Executed: true, Settled: true,
			})
			got := EvaluateWEA(obs, fixed)
			if len(got) != 1 || got[0].RuleID != RuleWEAUnauthorizedEffect || got[0].Disposition != DispositionContradiction || got[0].Reason != tc.wantReason {
				t.Fatalf("%s: got %+v, want contradiction %q", tc.name, got, tc.wantReason)
			}
		}

		// Absent required digest or scope identity reports DispositionIncomplete (never wildcards!).
		obsMissing := cleanWEAObservation()
		obsMissing.Grants = append(obsMissing.Grants, WEAGrantFact{
			GrantID: "g-missing-proj", Scope: "project", ActionDigest: "sha256:sc1",
			GrantedSeq: 8, Active: true, Settled: true,
		})
		obsMissing.AuthorityEffects = append(obsMissing.AuthorityEffects, WEAAuthorityEffectFact{
			EffectID: "eff-missing-proj", SessionID: obsMissing.SessionID, ProjectID: obsMissing.ProjectID,
			InvocationID: "inv-sc-1", ActionDigest: "sha256:sc1",
			GrantID: "g-missing-proj", EventSeq: 9, RequiresAuthority: true, Executed: true, Settled: true,
		})
		got := EvaluateWEA(obsMissing, fixed)
		if len(got) != 1 || got[0].RuleID != RuleWEAUnauthorizedEffect || got[0].Disposition != DispositionIncomplete {
			t.Fatalf("missing project_id on project grant must report incomplete, got %+v", got)
		}

		obsMissingDigest := cleanWEAObservation()
		obsMissingDigest.AuthorityEffects[0].ActionDigest = ""
		got = EvaluateWEA(obsMissingDigest, fixed)
		if len(got) != 1 || got[0].RuleID != RuleWEAUnauthorizedEffect || got[0].Disposition != DispositionIncomplete {
			t.Fatalf("missing effect digest must report incomplete, got %+v", got)
		}
	})

	t.Run("5a_egress_connection_unrecorded_by_product", func(t *testing.T) {
		obs := cleanWEAObservation()
		const rawURL = "https://exfil.fixture.invalid/leak?token=Bearer%20sk-ant-api03-verysecretvalue1234567890"
		obs.ObservedConnections = append(obs.ObservedConnections, WEAEgressConnectionFact{
			ConnectionID:   "conn-unrecorded-1",
			SessionID:      obs.SessionID,
			InvocationID:   "inv-egress-unrec",
			DestinationRef: rawURL,
			ObservedSeq:    20,
			Settled:        true,
			SourceRef:      "fixture_endpoint/conn-unrecorded-1.json",
		})

		findings := Evaluate(WrapWEAObservation(obs), fixed)
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

	t.Run("5b_egress_strict_ordering_cardinality_and_superseding_admission", func(t *testing.T) {
		// Missing invocation_id -> DispositionIncomplete (never wildcard!).
		obsMissingInv := cleanWEAObservation()
		obsMissingInv.ObservedConnections[0].InvocationID = ""
		got := EvaluateWEA(obsMissingInv, fixed)
		if len(got) != 1 || got[0].RuleID != RuleWEAUnrecordedOrRefusedEgress || got[0].Disposition != DispositionIncomplete {
			t.Fatalf("missing invocation_id must report incomplete, got %+v", got)
		}

		// Admission recorded AFTER observed connection -> contradiction.
		obsLateAdmit := cleanWEAObservation()
		obsLateAdmit.EgressDecisions[0].DecisionSeq = 25
		obsLateAdmit.ObservedConnections[0].ObservedSeq = 20
		got = EvaluateWEA(obsLateAdmit, fixed)
		if len(got) != 1 || got[0].RuleID != RuleWEAUnrecordedOrRefusedEgress || got[0].Disposition != DispositionContradiction || got[0].Reason != "egress_connection_before_admission" {
			t.Fatalf("expected egress_connection_before_admission contradiction, got %+v", got)
		}

		// Exact-action decision reused across a second arrival -> contradiction.
		obsReuse := cleanWEAObservation()
		obsReuse.ObservedConnections = append(obsReuse.ObservedConnections, WEAEgressConnectionFact{
			ConnectionID:   "conn-2-reuse",
			SessionID:      obsReuse.SessionID,
			InvocationID:   "inv-wea-1",
			DestinationRef: "https://fixture.invalid/api",
			ObservedSeq:    12,
			Settled:        true,
			SourceRef:      "fixture_endpoint/conn-2-reuse.json",
		})
		got = EvaluateWEA(obsReuse, fixed)
		if len(got) != 1 || got[0].RuleID != RuleWEAUnrecordedOrRefusedEgress || got[0].Disposition != DispositionContradiction || got[0].Reason != "egress_exact_decision_reused" {
			t.Fatalf("expected egress_exact_decision_reused contradiction, got %+v", got)
		}

		// Connection under a refused action -> contradiction.
		obsRefused := cleanWEAObservation()
		const deniedURL = "https://denied.fixture.invalid/post"
		obsRefused.EgressDecisions = append(obsRefused.EgressDecisions, WEAEgressDecisionFact{
			DecisionID:     "dec-refused-1",
			SessionID:      obsRefused.SessionID,
			InvocationID:   "inv-egress-refused",
			DestinationRef: deniedURL,
			Refused:        true,
			RefusalCode:    "WEB_SEARCH_HOST_DENIED",
			DecisionSeq:    30,
			SourceRef:      "egress/dec-refused-1.json",
		})
		obsRefused.ObservedConnections = append(obsRefused.ObservedConnections, WEAEgressConnectionFact{
			ConnectionID:   "conn-after-refusal-1",
			SessionID:      obsRefused.SessionID,
			InvocationID:   "inv-egress-refused",
			DestinationRef: deniedURL,
			ObservedSeq:    31,
			Settled:        true,
			SourceRef:      "fixture_endpoint/conn-after-refusal-1.json",
		})
		got = EvaluateWEA(obsRefused, fixed)
		if len(got) != 1 || got[0].RuleID != RuleWEAUnrecordedOrRefusedEgress || got[0].Disposition != DispositionContradiction || got[0].Reason != "egress_connection_after_refusal" {
			t.Fatalf("expected egress_connection_after_refusal contradiction, got %+v", got)
		}

		// Valid later admission superseding an earlier refusal -> healthy (0 findings!).
		obsSuperseded := cleanWEAObservation()
		obsSuperseded.EgressDecisions = append(obsSuperseded.EgressDecisions,
			WEAEgressDecisionFact{
				DecisionID:     "dec-refused-early",
				SessionID:      obsSuperseded.SessionID,
				InvocationID:   "inv-retry-1",
				DestinationRef: deniedURL,
				Refused:        true,
				RefusalCode:    "WEB_SEARCH_HOST_DENIED",
				DecisionSeq:    30,
			},
			WEAEgressDecisionFact{
				DecisionID:     "dec-admit-later",
				SessionID:      obsSuperseded.SessionID,
				InvocationID:   "inv-retry-1",
				DestinationRef: deniedURL,
				GrantScope:     "exact_action",
				Admitted:       true,
				DecisionSeq:    35,
			},
		)
		obsSuperseded.ObservedConnections = append(obsSuperseded.ObservedConnections, WEAEgressConnectionFact{
			ConnectionID:   "conn-after-superseding-admit",
			SessionID:      obsSuperseded.SessionID,
			InvocationID:   "inv-retry-1",
			DestinationRef: deniedURL,
			ObservedSeq:    36,
			Settled:        true,
		})
		if got := EvaluateWEA(obsSuperseded, fixed); len(got) != 0 {
			t.Fatalf("valid later admission superseding refusal must be healthy, got %+v", got)
		}
	})

	t.Run("6_managed_secret_canary_in_persisted_record", func(t *testing.T) {
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

		findings := Evaluate(WrapWEAObservation(obs), fixed)
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
		obs.AuthorityEffects[0].Settled = false
		obs.ApprovalPlans = nil
		obs.EgressDecisions = nil
		obs.ObservedConnections[0].Settled = false

		findings := Evaluate(WrapWEAObservation(obs), fixed)
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

		findings := Evaluate(WrapWEAObservation(obs), fixed)
		if len(findings) != 0 {
			t.Fatalf("late_card must produce 0 findings, got %+v", findings)
		}
	})

	t.Run("expected_refusal", func(t *testing.T) {
		obs := cleanWEAObservation()
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
		obs.AuthorityEffects = append(obs.AuthorityEffects, WEAAuthorityEffectFact{
			EffectID:          "eff-auth-refused",
			SessionID:         obs.SessionID,
			ActionKind:        "command",
			RequiresAuthority: true,
			Executed:          false,
			Refused:           true,
			RefusalCode:       "APPROVAL_DENIED",
		})
		obs.EgressDecisions = append(obs.EgressDecisions, WEAEgressDecisionFact{
			DecisionID:     "dec-egress-refused",
			SessionID:      obs.SessionID,
			InvocationID:   "inv-refused-egress",
			DestinationRef: "https://blocked.fixture.invalid/nope",
			Admitted:       false,
			Refused:        true,
			RefusalCode:    "WEB_SEARCH_HOST_DENIED",
			DecisionSeq:    40,
		})

		findings := Evaluate(WrapWEAObservation(obs), fixed)
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

		findings := Evaluate(WrapWEAObservation(obs), fixed)
		if len(findings) != 0 {
			t.Fatalf("retention_gap must produce 0 findings, got %+v", findings)
		}

		perFact := cleanWEAObservation()
		perFact.WorkerResults = nil
		perFact.LandedPaths[0].RetentionPruned = true
		perFact.ReadBases[0].RetentionPruned = true
		perFact.ApprovalPlans = nil
		perFact.AuthorityEffects[0].RetentionPruned = true
		perFact.EgressDecisions = nil
		perFact.ObservedConnections[0].RetentionPruned = true

		findings = Evaluate(WrapWEAObservation(perFact), fixed)
		if len(findings) != 0 {
			t.Fatalf("per-fact retention_gap must produce 0 findings, got %+v", findings)
		}
	})

	t.Run("clean_rebase", func(t *testing.T) {
		obs := cleanWEAObservation()
		obs.EditEffects[0].ReadBasisRevision = "rev-10"
		obs.EditEffects[0].CurrentRevision = "rev-12"
		obs.EditEffects[0].RebasedCleanly = true
		obs.EditEffects[0].RebasedFromRevision = "rev-10"

		findings := Evaluate(WrapWEAObservation(obs), fixed)
		if len(findings) != 0 {
			t.Fatalf("clean_rebase must produce 0 findings, got %+v", findings)
		}
	})
}

func TestWEAReusesExistingObserverLifecycleAndDisabledIsZeroAlloc(t *testing.T) {
	// 1. Disabled existing Observer is O(1) and 0 allocs/op.
	disabledObs := NewObserver(Config{Enabled: false})
	sample := cleanWEAObservation()
	sample.LandedPaths[0].PresentOnDisk = false
	wrapped := WrapWEAObservation(sample)

	var fetchCalls atomic.Int64
	task := Task{
		SessionID: sample.SessionID,
		ProgramID: sample.ResultHandle,
		Fetch: func(context.Context) (Observation, error) {
			fetchCalls.Add(1)
			return wrapped, nil
		},
	}

	if disabledObs.Submit(wrapped) {
		t.Fatal("disabled Observer.Submit must return false")
	}
	if disabledObs.SubmitTask(task) {
		t.Fatal("disabled Observer.SubmitTask must return false")
	}
	if fetchCalls.Load() != 0 {
		t.Fatalf("disabled Observer.SubmitTask invoked Fetch %d times", fetchCalls.Load())
	}
	if got := EvaluateWEAIfEnabled(false, sample, time.Now().UTC()); got != nil {
		t.Fatalf("EvaluateWEAIfEnabled(false) = %+v, want nil", got)
	}

	submitAllocs := testing.AllocsPerRun(100, func() {
		_ = disabledObs.Submit(wrapped)
	})
	if submitAllocs != 0 {
		t.Fatalf("disabled Observer.Submit allocated %v allocs/op, want 0", submitAllocs)
	}
	evalAllocs := testing.AllocsPerRun(100, func() {
		_ = EvaluateWEAIfEnabled(false, sample, time.Time{})
	})
	if evalAllocs != 0 {
		t.Fatalf("disabled EvaluateWEAIfEnabled allocated %v allocs/op, want 0", evalAllocs)
	}
	disabledObs.Close()

	// 2. Enabled existing Observer deduplicates WEA findings by (RuleID, ProgramID).
	fixed := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var got []Finding
	enabledObs := NewObserver(Config{
		Enabled:       true,
		QueueCapacity: 16,
		Now:           func() time.Time { return fixed },
		Sink: func(f Finding) {
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
		},
	})
	if !enabledObs.Submit(wrapped) || !enabledObs.Submit(wrapped) || !enabledObs.Submit(wrapped) {
		t.Fatal("expected enabled Observer to enqueue WEA observations")
	}
	enabledObs.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("expected 1 deduplicated finding from existing Observer, got %d: %+v", len(got), got)
	}
	if got[0].RuleID != RuleWEALandedPathMismatch || got[0].Disposition != DispositionContradiction {
		t.Fatalf("unexpected finding: %+v", got[0])
	}
	if stats := enabledObs.Stats(); stats.Emitted != 1 || stats.Deduplicated != 2 {
		t.Fatalf("unexpected observer stats: %+v", stats)
	}

	// 3. Default package ObserveWEA writes redacted JSONL via #2784 retention file when enabled,
	//    and writes nothing when disabled.
	outFile := filepath.Join(t.TempDir(), "wea-diagnostics.jsonl")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "")
	t.Setenv(debugpaths.FullDebugEnv, "")
	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS_FILE", outFile)
	Close()
	t.Cleanup(Close)

	if ObserveWEA(sample) {
		t.Fatal("expected ObserveWEA to return false when disabled")
	}
	if _, err := os.Stat(outFile); !os.IsNotExist(err) {
		t.Fatalf("expected no diagnostics file when disabled, stat err=%v", err)
	}

	t.Setenv("LYCAON_DEV_COMMAND_DIAGNOSTICS", "1")
	Close()
	if !ObserveWEA(sample) {
		t.Fatal("expected ObserveWEA to accept observation when enabled")
	}
	Close()
	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read wea diagnostics jsonl: %v", err)
	}
	var decoded Finding
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &decoded); err != nil {
		t.Fatalf("decode finding jsonl: %v", err)
	}
	if decoded.RuleID != RuleWEALandedPathMismatch || decoded.Disposition != DispositionContradiction {
		t.Fatalf("unexpected decoded finding from retention file: %+v", decoded)
	}
}

func TestWEAEnabledDisabledRuntimeParityAndBoundedBurst(t *testing.T) {
	fixed := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	// Simulate a product workflow that returns identical verdicts regardless of
	// whether opt-in WEA diagnostics are disabled or enabled.
	runProductWorkflow := func() (acceptedEdits int, allowedEgress int, refusedEgress int) {
		clean := cleanWEAObservation()
		// Healthy run + legitimate revalidation + superseding admission after refusal.
		clean.EditEffects[0].RebasedCleanly = true
		clean.EditEffects[0].CurrentRevision = "rev-15"
		clean.EgressDecisions = append(clean.EgressDecisions,
			WEAEgressDecisionFact{
				DecisionID: "dec-refuse-1", SessionID: clean.SessionID, InvocationID: "inv-reval",
				DestinationRef: "https://reval.fixture.invalid/v1", Refused: true, RefusalCode: "HOST_DENIED", DecisionSeq: 40,
			},
			WEAEgressDecisionFact{
				DecisionID: "dec-admit-2", SessionID: clean.SessionID, InvocationID: "inv-reval",
				DestinationRef: "https://reval.fixture.invalid/v1", GrantScope: "exact_action", Admitted: true, DecisionSeq: 45,
			},
		)
		clean.ObservedConnections = append(clean.ObservedConnections, WEAEgressConnectionFact{
			ConnectionID: "conn-reval", SessionID: clean.SessionID, InvocationID: "inv-reval",
			DestinationRef: "https://reval.fixture.invalid/v1", ObservedSeq: 46, Settled: true,
		})
		_ = ObserveWEA(clean)
		return 1, 2, 1
	}

	// 1. Disabled run: zero findings, zero side effects.
	restoreOff := SetObserver(NewObserver(Config{Enabled: false}))
	aOff, eOff, rOff := runProductWorkflow()
	restoreOff()

	// 2. Enabled run: identical product outcomes, zero warnings on healthy run.
	var mu sync.Mutex
	var got []Finding
	obsOn := NewObserver(Config{
		Enabled:       true,
		QueueCapacity: 16,
		Now:           func() time.Time { return fixed },
		Sink: func(f Finding) {
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
		},
	})
	restoreOn := SetObserver(obsOn)
	aOn, eOn, rOn := runProductWorkflow()
	restoreOn()
	obsOn.Close()

	if aOff != aOn || eOff != eOn || rOff != rOn {
		t.Fatalf("enabled/disabled product parity mismatch: off=(%d,%d,%d) on=(%d,%d,%d)", aOff, eOff, rOff, aOn, eOn, rOn)
	}
	mu.Lock()
	if len(got) != 0 {
		t.Fatalf("expected 0 warnings on healthy revalidation/supersession run, got %+v", got)
	}
	mu.Unlock()

	// 3. Bounded queue under burst: never blocks, explicitly records drops.
	gate := make(chan struct{})
	firstEntered := make(chan struct{})
	var closeGateOnce sync.Once
	closeGate := func() { closeGateOnce.Do(func() { close(gate) }) }
	var burstFindings []Finding
	burstObs := NewObserver(Config{
		Enabled:       true,
		QueueCapacity: 2,
		Now:           func() time.Time { return fixed },
		Sink: func(f Finding) {
			if f.RuleID == RuleWEALandedPathMismatch {
				select {
				case <-firstEntered:
				default:
					close(firstEntered)
				}
				<-gate
			}
			mu.Lock()
			burstFindings = append(burstFindings, f)
			mu.Unlock()
		},
	})
	t.Cleanup(func() {
		closeGate()
		burstObs.Close()
	})

	bad := cleanWEAObservation()
	bad.LandedPaths[0].PresentOnDisk = false
	if !burstObs.Submit(WrapWEAObservation(bad)) {
		t.Fatal("first burst item should enqueue")
	}
	<-firstEntered

	dropped := 0
	for i := 0; i < 32; i++ {
		if !burstObs.Submit(WrapWEAObservation(bad)) {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected bounded queue to drop excess WEA burst items without blocking")
	}
	closeGate()
	burstObs.Close()

	if burstObs.Stats().Dropped != int64(dropped) {
		t.Fatalf("stats.Dropped = %d, want %d", burstObs.Stats().Dropped, dropped)
	}
	mu.Lock()
	defer mu.Unlock()
	sawOverflow := false
	for _, f := range burstFindings {
		if f.RuleID == RuleCaptureOverflow && f.Disposition == DispositionOverflow && f.DroppedCount == int64(dropped) {
			sawOverflow = true
		}
	}
	if !sawOverflow {
		t.Fatalf("expected explicit capture_overflow finding under WEA burst, got %+v", burstFindings)
	}
}
