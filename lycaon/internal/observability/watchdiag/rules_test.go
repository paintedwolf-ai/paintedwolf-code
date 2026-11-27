package watchdiag

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
)

func cleanWatchObservation(base time.Time) Observation {
	return Observation{
		SessionID:   "11111111-2222-3333-4444-555555555555",
		WatchID:     "watch-clean-1",
		Handle:      "bg-proc-1",
		ClockDomain: ClockDomainManual,
		OwningSource: OwningSource{
			Component:   "watch_store",
			Phase:       "watch_snapshot",
			ClockDomain: ClockDomainManual,
			EvidenceRef: "ev-snapshot-1",
		},
		Deliveries: []DeliveryFact{
			{
				DeliveryID:      "deliv-1",
				WatchID:         "watch-clean-1",
				Handle:          "bg-proc-1",
				OccurrenceID:    "process_done:bg-proc-1",
				OwnerSessionID:  "11111111-2222-3333-4444-555555555555",
				RoutedSessionID: "11111111-2222-3333-4444-555555555555",
				Terminal:        true,
				Committed:       true,
				RequiresReport:  true,
				CommittedAt:     base.Add(2 * time.Second),
				CommittedSeq:    20,
				ClockDomain:     ClockDomainManual,
				OwningSource: OwningSource{
					Component:   "watch_delivery",
					Phase:       "terminal_delivery_commit",
					ClockDomain: ClockDomainManual,
					EvidenceRef: "ev-deliv-1",
				},
				EvidenceRef: "ev-deliv-1",
			},
		},
		Presentations: []PresentationFact{
			{
				ClaimID:         "claim-1",
				WatchID:         "watch-clean-1",
				Handle:          "bg-proc-1",
				OccurrenceID:    "process_done:bg-proc-1",
				OwnerSessionID:  "11111111-2222-3333-4444-555555555555",
				RoutedSessionID: "11111111-2222-3333-4444-555555555555",
				Kind:            "direct_wait",
				Committed:       true,
				TurnID:          "turn-1",
				ClockDomain:     ClockDomainManual,
				OwningSource: OwningSource{
					Component:   "watch_presentation",
					Phase:       "presentation_claim_commit",
					ClockDomain: ClockDomainManual,
					EvidenceRef: "ev-claim-1",
				},
				EvidenceRef: "ev-claim-1",
			},
		},
		PublicationPhases: []PublicationPhaseFact{
			{
				SessionID:    "11111111-2222-3333-4444-555555555555",
				Handle:       "bg-proc-1",
				OccurrenceID: "process_done:bg-proc-1",
				Phase:        PhaseProcessReportPublished,
				RecordedAt:   base.Add(1 * time.Second),
				RecordedSeq:  10,
				ClockDomain:  ClockDomainManual,
				OwningSource: OwningSource{
					Component:   "processcontrol",
					Phase:       PhaseProcessReportPublished,
					ClockDomain: ClockDomainManual,
					EvidenceRef: "ev-pub-1",
				},
				EvidenceRef: "ev-pub-1",
			},
		},
		StopFence: &StopFenceFact{
			SessionID:   "11111111-2222-3333-4444-555555555555",
			StopEpoch:   5,
			StoppedAt:   base.Add(10 * time.Second),
			ClockDomain: ClockDomainManual,
			OwningSource: OwningSource{
				Component:   "watch_stop_fence",
				Phase:       "stop_fence_commit",
				ClockDomain: ClockDomainManual,
				EvidenceRef: "ev-stop-5",
			},
			EvidenceRef: "ev-stop-5",
		},
		AdmittedTurns: []WatchTurnFact{
			{
				TurnID:          "turn-before-stop",
				SessionID:       "11111111-2222-3333-4444-555555555555",
				WatchID:         "watch-clean-1",
				Handle:          "bg-proc-1",
				OccurrenceID:    "process_done:bg-proc-1",
				ClaimID:         "claim-1",
				WatchOriginated: true,
				Admitted:        true,
				StopEpoch:       4,
				AdmittedAt:      base.Add(3 * time.Second),
				ClockDomain:     ClockDomainManual,
				OwningSource: OwningSource{
					Component:   "session_loop",
					Phase:       "turn_admission",
					ClockDomain: ClockDomainManual,
					EvidenceRef: "ev-turn-1",
				},
				EvidenceRef: "ev-turn-1",
			},
		},
	}
}

func TestEvaluateQuietOnCleanObservation(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	findings := Evaluate(cleanWatchObservation(base), base.Add(15*time.Second))
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings on clean observation, got %+v", findings)
	}
}

func TestEvaluateInjectedContradictions(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	evalAt := base.Add(15 * time.Second)

	t.Run("wrong_recipient_session_id", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.Deliveries[0].RoutedSessionID = "99999999-8888-7777-6666-555555555555"
		findings := Evaluate(obs, evalAt)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWrongRecipient || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "session_id_mismatch" {
			t.Fatalf("reason = %q, want session_id_mismatch", f.Reason)
		}
		if f.WatchID != "watch-clean-1" || f.Handle != "bg-proc-1" || f.OccurrenceID != "process_done:bg-proc-1" {
			t.Fatalf("missing source identities: %+v", f)
		}
		if f.EvidenceRef != "ev-deliv-1" || f.OwningSource.ClockDomain != ClockDomainManual {
			t.Fatalf("missing evidence_ref or clock_domain: %+v", f)
		}
		if len(f.Sources) != 1 || f.Sources[0].SessionID != observability.ShortSessionID("99999999-8888-7777-6666-555555555555") {
			t.Fatalf("unexpected source refs: %+v", f.Sources)
		}
	})

	t.Run("wrong_recipient_worker_job_id", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.WorkerJobID = "job-owner-1"
		obs.Deliveries[0].OwnerWorkerJobID = "job-owner-1"
		obs.Deliveries[0].RoutedWorkerJobID = "job-owner-1"
		obs.Presentations[0].OwnerWorkerJobID = "job-owner-1"
		obs.Presentations[0].RoutedWorkerJobID = "job-wrong-2"

		findings := Evaluate(obs, evalAt)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWrongRecipient || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "worker_job_id_mismatch" || f.ClaimID != "claim-1" {
			t.Fatalf("unexpected worker job mismatch finding: %+v", f)
		}
		if f.Expected["owner_worker_job_id"] != "job-owner-1" || f.Observed["routed_worker_job_id"] != "job-wrong-2" {
			t.Fatalf("unexpected expected/observed: expected=%+v observed=%+v", f.Expected, f.Observed)
		}
	})

	t.Run("duplicate_committed_presentation", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.Presentations = append(obs.Presentations, PresentationFact{
			ClaimID:         "claim-dup-2",
			WatchID:         "watch-clean-1",
			Handle:          "bg-proc-1",
			OccurrenceID:    "process_done:bg-proc-1",
			OwnerSessionID:  obs.SessionID,
			RoutedSessionID: obs.SessionID,
			Kind:            "watch_evidence",
			Committed:       true,
			TurnID:          "turn-2",
			ClockDomain:     ClockDomainSystem,
			OwningSource: OwningSource{
				Component:   "watch_presentation",
				Phase:       "presentation_claim_commit",
				ClockDomain: ClockDomainSystem,
				EvidenceRef: "ev-claim-2",
			},
			EvidenceRef: "ev-claim-2",
		})
		findings := Evaluate(obs, evalAt)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleDuplicateCommittedPresentation || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if len(f.Sources) != 2 || f.Sources[0].ClaimID != "claim-1" || f.Sources[1].ClaimID != "claim-dup-2" {
			t.Fatalf("expected both committed presentation sources, got %+v", f.Sources)
		}
		if f.Observed["max_committed_count"] != 2 {
			t.Fatalf("expected max_committed_count=2, got %+v", f.Observed)
		}
	})

	t.Run("terminal_delivery_before_report_missing_phase", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.PublicationPhases = nil
		findings := Evaluate(obs, evalAt)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleTerminalDeliveryBeforeReport || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "missing_process_report_published_phase" {
			t.Fatalf("reason = %q, want missing_process_report_published_phase", f.Reason)
		}
		if f.Expected["required_phase"] != PhaseProcessReportPublished {
			t.Fatalf("expected required_phase=%s, got %+v", PhaseProcessReportPublished, f.Expected)
		}
		if f.Observed["missing_report_count"] != 1 {
			t.Fatalf("expected missing_report_count=1, got %+v", f.Observed)
		}
	})

	t.Run("terminal_delivery_before_report_premature_order", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.Deliveries[0].CommittedSeq = 5
		obs.Deliveries[0].CommittedAt = base.Add(500 * time.Millisecond)
		obs.PublicationPhases[0].RecordedSeq = 10
		obs.PublicationPhases[0].RecordedAt = base.Add(1 * time.Second)

		findings := Evaluate(obs, evalAt)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleTerminalDeliveryBeforeReport || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Reason != "terminal_delivery_committed_before_report_published" {
			t.Fatalf("reason = %q", f.Reason)
		}
		if len(f.Sources) != 2 {
			t.Fatalf("expected delivery + publication phase source refs, got %+v", f.Sources)
		}
		if f.Sources[1].OwningSource.Phase != PhaseProcessReportPublished || f.Sources[1].OwningSource.ClockDomain != ClockDomainManual {
			t.Fatalf("publication source ref missing phase or clock domain: %+v", f.Sources[1])
		}
	})

	t.Run("watch_turn_past_stop_fence_epoch", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.AdmittedTurns = append(obs.AdmittedTurns, WatchTurnFact{
			TurnID:          "turn-after-stop-epoch",
			SessionID:       obs.SessionID,
			WatchID:         "watch-clean-1",
			Handle:          "bg-proc-1",
			OccurrenceID:    "process_done:bg-proc-1",
			ClaimID:         "claim-after-stop",
			WatchOriginated: true,
			Admitted:        true,
			StopEpoch:       5,
			AdmittedAt:      base.Add(11 * time.Second),
			ClockDomain:     ClockDomainManual,
			OwningSource: OwningSource{
				Component:   "session_loop",
				Phase:       "turn_admission",
				ClockDomain: ClockDomainManual,
				EvidenceRef: "ev-turn-bad",
			},
			EvidenceRef: "ev-turn-bad",
		})
		findings := Evaluate(obs, evalAt)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleWatchTurnPastStopFence || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.TurnID != "turn-after-stop-epoch" || f.StopEpoch != 5 {
			t.Fatalf("missing turn_id or stop_epoch: %+v", f)
		}
		if len(f.Sources) != 2 || f.Sources[1].OwningSource.Phase != "stop_fence_commit" {
			t.Fatalf("expected turn + stop fence sources, got %+v", f.Sources)
		}
	})

	t.Run("watch_turn_past_stop_fence_timestamp", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.StopFence.StopEpoch = 0
		obs.AdmittedTurns = []WatchTurnFact{
			{
				TurnID:          "turn-after-stop-ts",
				SessionID:       obs.SessionID,
				WatchID:         "watch-clean-1",
				WatchOriginated: true,
				Admitted:        true,
				AdmittedAt:      base.Add(12 * time.Second),
				ClockDomain:     ClockDomainManual,
				EvidenceRef:     "ev-turn-ts",
			},
		}
		findings := Evaluate(obs, evalAt)
		if len(findings) != 1 || findings[0].RuleID != RuleWatchTurnPastStopFence || findings[0].Reason != "watch_turn_admitted_after_stop_timestamp" {
			t.Fatalf("expected timestamp stop fence contradiction, got %+v", findings)
		}
	})
}

func TestEvaluateZeroFalsePositivesOnBenignEvents(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	evalAt := base.Add(20 * time.Second)

	t.Run("healthy_lag_uncommitted_delivery_and_claim", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.LagPending = true
		obs.Deliveries[0].Committed = false
		obs.PublicationPhases = nil
		obs.Presentations = append(obs.Presentations, PresentationFact{
			ClaimID:         "claim-queued",
			WatchID:         obs.WatchID,
			Handle:          obs.Handle,
			OccurrenceID:    "process_done:bg-proc-1",
			OwnerSessionID:  obs.SessionID,
			RoutedSessionID: obs.SessionID,
			Committed:       false,
		})
		if got := Evaluate(obs, evalAt); len(got) != 0 {
			t.Fatalf("healthy lag raised findings: %+v", got)
		}
	})

	t.Run("retention_gap_pruned_phase_record", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.RetentionGap = true
		obs.PublicationPhases = nil
		if got := Evaluate(obs, evalAt); len(got) != 0 {
			t.Fatalf("retention gap raised findings: %+v", got)
		}
	})

	t.Run("late_receipt_lookup_and_history_reread", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.Presentations = append(obs.Presentations,
			PresentationFact{
				ClaimID:             "claim-1",
				WatchID:             obs.WatchID,
				Handle:              obs.Handle,
				OccurrenceID:        "process_done:bg-proc-1",
				OwnerSessionID:      obs.SessionID,
				RoutedSessionID:     obs.SessionID,
				Committed:           true,
				IsLateReceiptLookup: true,
			},
			PresentationFact{
				ClaimID:         "claim-1",
				WatchID:         obs.WatchID,
				Handle:          obs.Handle,
				OccurrenceID:    "process_done:bg-proc-1",
				OwnerSessionID:  obs.SessionID,
				RoutedSessionID: obs.SessionID,
				Committed:       true,
				IsHistoryReread: true,
			},
		)
		if got := Evaluate(obs, evalAt); len(got) != 0 {
			t.Fatalf("late receipt lookup / history reread raised findings: %+v", got)
		}
	})

	t.Run("healthy_coalescing", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.CoalescedCount = 4
		obs.Presentations[0].CoalescedFrom = []string{"occ-1", "occ-2", "occ-3", "occ-4"}
		if got := Evaluate(obs, evalAt); len(got) != 0 {
			t.Fatalf("healthy coalescing raised findings: %+v", got)
		}
	})

	t.Run("sse_reconnect_repaint", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.ReconnectRepaint = true
		obs.Presentations = append(obs.Presentations, PresentationFact{
			ClaimID:            "claim-1",
			WatchID:            obs.WatchID,
			Handle:             obs.Handle,
			OccurrenceID:       "process_done:bg-proc-1",
			OwnerSessionID:     obs.SessionID,
			RoutedSessionID:    obs.SessionID,
			Committed:          true,
			IsReconnectRepaint: true,
		})
		if got := Evaluate(obs, evalAt); len(got) != 0 {
			t.Fatalf("SSE reconnect repaint raised findings: %+v", got)
		}
	})

	t.Run("non_watch_user_turn_and_unadmitted_turn_after_stop", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.AdmittedTurns = append(obs.AdmittedTurns,
			WatchTurnFact{
				TurnID:          "user-turn-after-stop",
				SessionID:       obs.SessionID,
				WatchOriginated: false,
				Admitted:        true,
				StopEpoch:       6,
				AdmittedAt:      base.Add(15 * time.Second),
				ClockDomain:     ClockDomainManual,
			},
			WatchTurnFact{
				TurnID:          "watch-turn-blocked-by-stop",
				SessionID:       obs.SessionID,
				WatchID:         obs.WatchID,
				WatchOriginated: true,
				Admitted:        false,
				StopEpoch:       6,
				AdmittedAt:      base.Add(16 * time.Second),
				ClockDomain:     ClockDomainManual,
			},
		)
		if got := Evaluate(obs, evalAt); len(got) != 0 {
			t.Fatalf("non-watch or unadmitted turn raised findings: %+v", got)
		}
	})

	t.Run("mismatched_clock_domains_without_seq_do_not_false_positive", func(t *testing.T) {
		obs := cleanWatchObservation(base)
		obs.Deliveries[0].CommittedSeq = 0
		obs.Deliveries[0].ClockDomain = ClockDomainSystem
		obs.Deliveries[0].CommittedAt = base.Add(-1 * time.Hour)
		obs.PublicationPhases[0].RecordedSeq = 0
		obs.PublicationPhases[0].ClockDomain = ClockDomainManual
		obs.PublicationPhases[0].RecordedAt = base
		if got := Evaluate(obs, evalAt); len(got) != 0 {
			t.Fatalf("mismatched clock domains without sequence raised findings: %+v", got)
		}
	})
}

func TestSanitizedSyntheticWatchReplay(t *testing.T) {
	const syntheticJSON = `{
		"session_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"watch_id": "watch-synth-1",
		"handle": "bg-synth-1",
		"clock_domain": "system",
		"deliveries": [{
			"delivery_id": "deliv-synth-1",
			"watch_id": "watch-synth-1",
			"handle": "bg-synth-1",
			"occurrence_id": "process_done:bg-synth-1",
			"owner_session_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"routed_session_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"terminal": true,
			"committed": true,
			"requires_report": true,
			"clock_domain": "system",
			"owning_source": {
				"component": "watch_delivery",
				"phase": "terminal_delivery_commit",
				"clock_domain": "system",
				"evidence_ref": "ev-synth-deliv-1"
			}
		}]
	}`

	var obs Observation
	testutil.FailErr(t, "decode synthetic watch observation", json.Unmarshal([]byte(syntheticJSON), &obs))
	findings := Evaluate(obs, time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC))
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding from synthetic replay, got %+v", findings)
	}
	f := findings[0]
	if f.RuleID != RuleTerminalDeliveryBeforeReport || f.Disposition != DispositionContradiction {
		t.Fatalf("unexpected finding: %+v", f)
	}
	wantSess := observability.ShortSessionID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if f.SessionID != wantSess || f.OwningSource.ClockDomain != ClockDomainSystem {
		t.Fatalf("expected short session %q and system clock domain, got %+v", wantSess, f)
	}
}
