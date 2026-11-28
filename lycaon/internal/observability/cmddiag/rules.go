package cmddiag

import (
	"slices"
	"time"
)

// Evaluate runs the four high-confidence checks, the gated Stop-admission
// check, and observation-loss checks against an immutable Observation.
// At most one structured finding is emitted per rule and program.
func Evaluate(obs Observation, now time.Time) []Finding {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var out []Finding
	if obs.WEA == nil || hasCommandFacts(obs) {
		if f, ok := checkObservationLoss(obs, now); ok {
			out = append(out, sanitizeFinding(f))
		}
		if f, ok := checkDupOrConflictingTerminal(obs, now); ok {
			out = append(out, sanitizeFinding(f))
		}
		if f, ok := checkMissingChildTerminal(obs, now); ok {
			out = append(out, sanitizeFinding(f))
		}
		if f, ok := checkStaleTerminalPresentation(obs, now); ok {
			out = append(out, sanitizeFinding(f))
		}
		if f, ok := checkOperationSourceContradiction(obs, now); ok {
			out = append(out, sanitizeFinding(f))
		}
		if f, ok := checkStopAdmissionAfterAck(obs, now); ok {
			out = append(out, sanitizeFinding(f))
		}
	}
	if obs.WEA != nil {
		out = append(out, EvaluateWEA(*obs.WEA, now)...)
	}
	return out
}

func hasCommandFacts(obs Observation) bool {
	return obs.ProgramState != "" ||
		obs.ProgramSettled ||
		obs.ProgramStatusCode != nil ||
		obs.ProgramStopCode != "" ||
		obs.PageInconsistent ||
		len(obs.LookupFailedExecutions) > 0 ||
		obs.Presentation != nil ||
		obs.PresentationUnsupported ||
		len(obs.Executions) > 0 ||
		len(obs.TerminalRecords) > 0 ||
		len(obs.LinkedOperations) > 0 ||
		obs.StopAck != nil
}

func baseFinding(obs Observation, now time.Time, rule RuleID, disp Disposition) Finding {
	return Finding{
		SchemaVersion: SchemaVersion,
		Timestamp:     now.UTC(),
		RuleID:        rule,
		Disposition:   disp,
		SessionID:     obs.SessionID,
		ProgramID:     obs.ProgramID,
		ParentReceipt: obs.ParentReceiptID,
		ToolCallID:    obs.ToolCallID,
	}
}

func checkObservationLoss(obs Observation, now time.Time) (Finding, bool) {
	if obs.PageInconsistent {
		f := baseFinding(obs, now, RuleObservationLoss, DispositionIncomplete)
		f.ExecutionIDs = obs.LookupFailedExecutions
		f.Reason = "inconsistent_receipt_pages"
		f.Expected = map[string]any{"consistent_receipt_pages": true}
		f.Observed = map[string]any{"page_inconsistent": true, "lookup_failures": len(obs.LookupFailedExecutions)}
		return f, true
	}
	if len(obs.LookupFailedExecutions) > 0 {
		f := baseFinding(obs, now, RuleObservationLoss, DispositionIncomplete)
		f.ExecutionIDs = obs.LookupFailedExecutions
		f.Reason = "revision_pin_lookup_failed"
		f.Expected = map[string]any{"lookup_failures": 0}
		f.Observed = map[string]any{"lookup_failures": len(obs.LookupFailedExecutions)}
		return f, true
	}
	return Finding{}, false
}

func checkDupOrConflictingTerminal(obs Observation, now time.Time) (Finding, bool) {
	records := obs.TerminalRecords
	if len(records) == 0 {
		for _, ex := range obs.Executions {
			if IsTerminalState(ex.State) {
				records = append(records, TerminalFact{
					ExecutionID:     ex.ExecutionID,
					State:           ex.State,
					StatusCode:      ex.StatusCode,
					StopCode:        ex.StopCode,
					HostStopCode:    ex.HostStopCode,
					EffectCertainty: ex.EffectCertainty,
					Source:          "receipt",
				})
			}
		}
	}
	byExec := make(map[string][]TerminalFact, len(records))
	for _, rec := range records {
		if rec.ExecutionID == "" || !IsTerminalState(rec.State) {
			continue
		}
		byExec[rec.ExecutionID] = append(byExec[rec.ExecutionID], rec)
	}
	var dupExecs, states []string
	var conflicting int
	for execID, list := range byExec {
		if len(list) <= 1 {
			continue
		}
		dupExecs = append(dupExecs, execID)
		first := list[0]
		conflict := false
		for _, other := range list[1:] {
			if !sameTerminalOutcome(first, other) {
				conflict = true
			}
		}
		if conflict {
			conflicting++
		}
		for _, item := range list {
			states = append(states, item.State)
		}
	}
	if len(dupExecs) == 0 {
		return Finding{}, false
	}
	f := baseFinding(obs, now, RuleDupOrConflictingTerminal, DispositionContradiction)
	f.ExecutionIDs = dupExecs
	f.Expected = map[string]any{"terminal_records_per_execution": 1}
	f.Observed = map[string]any{
		"duplicate_execution_count":   len(dupExecs),
		"conflicting_execution_count": conflicting,
		"observed_states":             states,
	}
	f.Reason = "duplicate_or_conflicting_terminal_facts"
	return f, true
}

func sameTerminalOutcome(a, b TerminalFact) bool {
	return a.State == b.State &&
		sameIntPtr(a.StatusCode, b.StatusCode) &&
		a.StopCode == b.StopCode &&
		a.HostStopCode == b.HostStopCode &&
		a.EffectCertainty == b.EffectCertainty
}

func checkMissingChildTerminal(obs Observation, now time.Time) (Finding, bool) {
	if !obs.ProgramSettled || !IsTerminalState(obs.ProgramState) {
		f := baseFinding(obs, now, RuleMissingChildTerminal, DispositionIncomplete)
		f.Reason = "program_not_declared_complete"
		f.Expected = map[string]any{"program_settled": true}
		f.Observed = map[string]any{"program_settled": obs.ProgramSettled, "program_state": obs.ProgramState}
		return f, true
	}
	if obs.PageInconsistent {
		return Finding{}, false
	}
	if obs.Truncated || obs.ExecutionsOmitted > 0 {
		f := baseFinding(obs, now, RuleMissingChildTerminal, DispositionIncomplete)
		f.Reason = "truncated_execution_observation"
		f.Expected = map[string]any{"executions_omitted": 0, "truncated": false}
		f.Observed = map[string]any{"executions_omitted": obs.ExecutionsOmitted, "truncated": obs.Truncated}
		return f, true
	}
	var missingExecs, opIDs, states []string
	for _, ex := range obs.Executions {
		if !ex.Admitted {
			continue
		}
		if !IsTerminalState(ex.State) {
			missingExecs = append(missingExecs, ex.ExecutionID)
			if ex.OperationID != "" {
				opIDs = append(opIDs, ex.OperationID)
			}
			states = append(states, ex.State)
		}
	}
	if len(missingExecs) == 0 {
		return Finding{}, false
	}
	f := baseFinding(obs, now, RuleMissingChildTerminal, DispositionContradiction)
	f.ExecutionIDs = missingExecs
	f.OperationIDs = opIDs
	f.Expected = map[string]any{"admitted_children_terminal": true, "program_state": obs.ProgramState}
	f.Observed = map[string]any{"missing_terminal_count": len(missingExecs), "non_terminal_states": states}
	f.Reason = "admitted_child_missing_terminal_record"
	return f, true
}

func checkStaleTerminalPresentation(obs Observation, now time.Time) (Finding, bool) {
	if !obs.ProgramSettled || !IsTerminalState(obs.ProgramState) {
		return Finding{}, false
	}
	if obs.PresentationUnsupported {
		f := baseFinding(obs, now, RuleStaleTerminalPresentation, DispositionUnsupported)
		f.Reason = "no_independent_presentation_witness"
		f.Expected = map[string]any{"independent_presentation_witness": true}
		f.Observed = map[string]any{"independent_presentation_witness": false}
		return f, true
	}
	pres := obs.Presentation
	if pres == nil {
		f := baseFinding(obs, now, RuleStaleTerminalPresentation, DispositionIncomplete)
		f.Reason = "presentation_unavailable"
		f.Expected = map[string]any{"presentation_present": true}
		f.Observed = map[string]any{"presentation_present": false}
		return f, true
	}
	if pres.HostBoundaryOnly {
		if obs.ActivityRevision > 0 && pres.ActivityRevision != obs.ActivityRevision {
			f := baseFinding(obs, now, RuleStaleTerminalPresentation, DispositionIncomplete)
			f.Reason = "host_snapshot_revision_drift"
			f.Expected = map[string]any{"activity_revision": obs.ActivityRevision}
			f.Observed = map[string]any{"activity_revision": pres.ActivityRevision, "host_boundary_only": true}
			return f, true
		}
	} else if !obs.ConsumerCaughtUp {
		f := baseFinding(obs, now, RuleStaleTerminalPresentation, DispositionIncomplete)
		f.Reason = "consumer_not_caught_up"
		f.Expected = map[string]any{"consumer_caught_up": true}
		f.Observed = map[string]any{"consumer_caught_up": false}
		return f, true
	}
	var reasons, staleExecs []string
	if !IsTerminalState(pres.ProgramState) || pres.ProgramState != obs.ProgramState {
		reasons = append(reasons, "program_state_mismatch")
	}
	if !sameIntPtr(pres.ProgramStatusCode, obs.ProgramStatusCode) {
		reasons = append(reasons, "program_status_mismatch")
	}
	if pres.ProgramStopCode != obs.ProgramStopCode {
		reasons = append(reasons, "program_stop_code_mismatch")
	}
	if !pres.HostBoundaryOnly && obs.ActivityRevision > 0 && pres.ActivityRevision < obs.ActivityRevision {
		reasons = append(reasons, "stale_activity_revision")
	}
	for _, act := range pres.Activities {
		if !IsTerminalState(act.State) {
			reasons = append(reasons, "non_terminal_activity")
			staleExecs = append(staleExecs, act.ExecutionIDs...)
		}
	}
	if len(reasons) > 0 {
		slices.Sort(reasons)
		reasons = slices.Compact(reasons)
		f := baseFinding(obs, now, RuleStaleTerminalPresentation, DispositionContradiction)
		f.ExecutionIDs = staleExecs
		f.Expected = map[string]any{
			"program_state":     obs.ProgramState,
			"status_code":       obs.ProgramStatusCode,
			"stop_code":         obs.ProgramStopCode,
			"activity_revision": obs.ActivityRevision,
		}
		f.Observed = map[string]any{
			"program_state":      pres.ProgramState,
			"status_code":        pres.ProgramStatusCode,
			"stop_code":          pres.ProgramStopCode,
			"activity_revision":  pres.ActivityRevision,
			"host_boundary_only": pres.HostBoundaryOnly,
			"mismatches":         reasons,
		}
		f.Reason = reasons[0]
		return f, true
	}
	if pres.Truncated {
		f := baseFinding(obs, now, RuleStaleTerminalPresentation, DispositionIncomplete)
		f.Reason = "truncated_presentation_snapshot"
		f.Expected = map[string]any{"truncated": false}
		f.Observed = map[string]any{"truncated": true, "activity_count": pres.ActivityCount, "sampled_activities": len(pres.Activities)}
		return f, true
	}
	return Finding{}, false
}

func sameIntPtr(a, b *int) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
