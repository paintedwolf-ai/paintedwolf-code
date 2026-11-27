package cmddiag

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/testutil"
)

func intPtr(v int) *int { return &v }

func cleanSettledObservation() Observation {
	return Observation{
		SessionID:         "11111111-2222-3333-4444-555555555555",
		ProgramID:         "prog-clean-1",
		ParentReceiptID:   "rcpt-clean-1",
		ToolCallID:        "call-clean-1",
		ProgramState:      "completed",
		ProgramStatusCode: intPtr(0),
		ProgramSettled:    true,
		ActivityRevision:  4,
		ConsumerCaughtUp:  true,
		Presentation: &PresentationSnapshot{
			ProgramState:      "completed",
			ProgramStatusCode: intPtr(0),
			ActivityRevision:  4,
			Activities: []ActivityFact{
				{ActivityID: "act-1", State: "completed", StatusCode: intPtr(0), ExecutionIDs: []string{"exec-1"}},
			},
		},
		Executions: []ExecutionFact{
			{
				ExecutionID:       "exec-1",
				OperationID:       "write",
				Owner:             "native",
				State:             "completed",
				Admitted:          true,
				OwnerStarted:      true,
				OwnerInvoked:      true,
				StatusCode:        intPtr(0),
				EffectCertainty:   "completed",
				PublishedRevision: "rev-after-1",
			},
		},
		LinkedOperations: []OperationSourceLink{
			{
				ExecutionID:         "exec-1",
				OperationID:         "write",
				SourceID:            "root-1:src/app.go",
				ExpectedOperationID: "write",
				ExpectedSourceID:    "root-1:src/app.go",
				ExpectedRevision:    "rev-after-1",
				ObservedRevision:    "rev-after-1",
				Completed:           true,
				CompleteEvidence:    true,
			},
		},
	}
}

func TestEvaluateQuietOnCleanSettledRun(t *testing.T) {
	findings := Evaluate(cleanSettledObservation(), time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC))
	if len(findings) != 0 {
		t.Fatalf("expected quiet clean run (0 findings), got %+v", findings)
	}
}

func TestEvaluateSeededContradictions(t *testing.T) {
	fixed := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	t.Run("dup_or_conflicting_terminal", func(t *testing.T) {
		obs := cleanSettledObservation()
		obs.TerminalRecords = []TerminalFact{
			{ExecutionID: "exec-1", State: "completed", StatusCode: intPtr(0), Source: "receipt"},
			{ExecutionID: "exec-1", State: "failed", StatusCode: intPtr(1), Source: "projection"},
		}
		findings := Evaluate(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleDupOrConflictingTerminal || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.ProgramID != "prog-clean-1" || len(f.ExecutionIDs) != 1 || f.ExecutionIDs[0] != "exec-1" {
			t.Fatalf("missing owning IDs: %+v", f)
		}
		if f.Observed["conflicting_execution_count"] != 1 {
			t.Fatalf("expected conflicting_execution_count=1, got %+v", f.Observed)
		}
	})

	t.Run("missing_child_terminal", func(t *testing.T) {
		obs := cleanSettledObservation()
		obs.Executions = append(obs.Executions, ExecutionFact{
			ExecutionID:  "exec-2",
			OperationID:  "grep",
			State:        "running",
			Admitted:     true,
			OwnerStarted: true,
		})
		findings := Evaluate(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleMissingChildTerminal || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if len(f.ExecutionIDs) != 1 || f.ExecutionIDs[0] != "exec-2" || len(f.OperationIDs) != 1 || f.OperationIDs[0] != "grep" {
			t.Fatalf("missing owning IDs: %+v", f)
		}
	})

	t.Run("stale_terminal_presentation", func(t *testing.T) {
		obs := cleanSettledObservation()
		obs.Presentation.ProgramState = "running"
		obs.Presentation.ActivityRevision = 2
		obs.Presentation.Activities[0].State = "running"
		findings := Evaluate(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleStaleTerminalPresentation || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if f.Expected["program_state"] != "completed" || f.Observed["program_state"] != "running" {
			t.Fatalf("missing expected/observed facts: %+v", f)
		}
	})

	t.Run("operation_source_contradiction", func(t *testing.T) {
		obs := cleanSettledObservation()
		obs.LinkedOperations[0].ObservedRevision = "rev-wrong-99"
		obs.LinkedOperations[0].SourceID = "root-1:src/other.go"
		findings := Evaluate(obs, fixed)
		if len(findings) != 1 {
			t.Fatalf("expected 1 finding, got %+v", findings)
		}
		f := findings[0]
		if f.RuleID != RuleOperationSourceContradiction || f.Disposition != DispositionContradiction {
			t.Fatalf("unexpected finding: %+v", f)
		}
		if len(f.ExecutionIDs) != 1 || f.ExecutionIDs[0] != "exec-1" || len(f.SourceIDs) != 2 {
			t.Fatalf("missing owning source/exec IDs: %+v", f)
		}
	})
}

func TestEvaluateStopAdmissionGating(t *testing.T) {
	fixed := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	// Without a qualified Stop ack or comparable witness, rule stays gated and reports unsupported.
	obs := cleanSettledObservation()
	obs.StopAck = &StopAckWitness{
		Qualified:                  false,
		AckOrdinal:                 10,
		AdmissionWitnessComparable: false,
	}
	findings := Evaluate(obs, fixed)
	if len(findings) != 1 || findings[0].RuleID != RuleStopAdmissionAfterAck || findings[0].Disposition != DispositionUnsupported {
		t.Fatalf("expected gated unsupported finding, got %+v", findings)
	}

	// With both qualified Stop ack and comparable admission ordinal witness, post-ack admission is caught.
	obs.StopAck = &StopAckWitness{
		Qualified:                  true,
		AckOrdinal:                 10,
		AdmissionWitnessComparable: true,
		StopRef:                    "stop-ack-1",
	}
	obs.Executions[0].AdmissionOrdinal = 12
	findings = Evaluate(obs, fixed)
	if len(findings) != 1 || findings[0].RuleID != RuleStopAdmissionAfterAck || findings[0].Disposition != DispositionContradiction {
		t.Fatalf("expected stop admission contradiction, got %+v", findings)
	}
	if len(findings[0].ExecutionIDs) != 1 || findings[0].ExecutionIDs[0] != "exec-1" {
		t.Fatalf("missing execution ID: %+v", findings[0])
	}
}

func TestEvaluateLifecycleLagAndTruncationDispositions(t *testing.T) {
	fixed := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

	// Unsettled program: lifecycle lag -> incomplete (not a contradiction).
	unsettled := cleanSettledObservation()
	unsettled.ProgramSettled = false
	unsettled.ProgramState = "running"
	unsettled.Executions[0].State = "running"
	findings := Evaluate(unsettled, fixed)
	if len(findings) != 1 || findings[0].RuleID != RuleMissingChildTerminal || findings[0].Disposition != DispositionIncomplete {
		t.Fatalf("expected incomplete for lifecycle lag, got %+v", findings)
	}

	// Truncated paged observation -> incomplete.
	truncated := cleanSettledObservation()
	truncated.Truncated = true
	truncated.ExecutionsOmitted = 5
	findings = Evaluate(truncated, fixed)
	if len(findings) != 1 || findings[0].RuleID != RuleMissingChildTerminal || findings[0].Disposition != DispositionIncomplete {
		t.Fatalf("expected incomplete for truncated observation, got %+v", findings)
	}

	// Delayed consumer projection -> incomplete.
	delayed := cleanSettledObservation()
	delayed.ConsumerCaughtUp = false
	findings = Evaluate(delayed, fixed)
	if len(findings) != 1 || findings[0].RuleID != RuleStaleTerminalPresentation || findings[0].Disposition != DispositionIncomplete {
		t.Fatalf("expected incomplete for delayed presentation, got %+v", findings)
	}
}

func TestSanitizedSyntheticReconstructionReplay(t *testing.T) {
	// Sanitized synthetic observation JSON contains only metadata and opaque IDs
	// (no raw command args, env, stdout/stderr, or file contents).
	const syntheticJSON = `{
		"session_id": "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"program_id": "prog-synth-replay-1",
		"parent_receipt_id": "rcpt-synth-1",
		"tool_call_id": "call-synth-1",
		"program_state": "completed",
		"program_status_code": 0,
		"program_settled": true,
		"activity_revision": 3,
		"consumer_caught_up": true,
		"presentation": {
			"program_state": "completed",
			"program_status_code": 0,
			"activity_revision": 3,
			"activities": [{"activity_id": "act-s1", "state": "completed", "status_code": 0, "execution_ids": ["exec-s1"]}]
		},
		"executions": [
			{"execution_id": "exec-s1", "operation_id": "write", "state": "running", "admitted": true, "owner_started": true}
		]
	}`
	var obs Observation
	testutil.FailErr(t, "decode synthetic reconstruction", json.Unmarshal([]byte(syntheticJSON), &obs))
	findings := Evaluate(obs, time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC))
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding from synthetic replay, got %+v", findings)
	}
	if findings[0].RuleID != RuleMissingChildTerminal || findings[0].Disposition != DispositionContradiction {
		t.Fatalf("unexpected replay finding: %+v", findings[0])
	}
	wantSession := observability.ShortSessionID("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if findings[0].SessionID != wantSession || findings[0].ProgramID != "prog-synth-replay-1" {
		t.Fatalf("expected short redacted session ID %q and program ID, got %+v", wantSession, findings[0])
	}
}
