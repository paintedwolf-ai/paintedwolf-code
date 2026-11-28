package portablepython

import (
	"bytes"
	"fmt"
)

// SplitPreservationObs captures split-regression state across invalid, overlapping, and valid runs.
type SplitPreservationObs struct {
	Original        map[string][]byte
	AfterInvalid    map[string][]byte
	InvalidRejected bool
	AfterOverlap    map[string][]byte
	OverlapRejected bool
	ExpectedValid   map[string][]byte
	ValidRun1       map[string][]byte
	ValidRun2       map[string][]byte
}

func requireExactFileMap(label string, want, got map[string][]byte) error {
	if len(got) != len(want) {
		return fmt.Errorf("%s file count mismatch: want %d, got %d", label, len(want), len(got))
	}
	for name, wantBytes := range want {
		gotBytes, exists := got[name]
		if !exists {
			return fmt.Errorf("%s missing expected file %s", label, name)
		}
		if !bytes.Equal(gotBytes, wantBytes) {
			return fmt.Errorf("%s byte mismatch on %s", label, name)
		}
	}
	return nil
}

// GradeSplitPreservation verifies rejection of invalid/overlapping inputs, exact preservation of
// pre-existing split files (distinguishing missing from empty), and non-empty deterministic valid output.
func GradeSplitPreservation(obs SplitPreservationObs) error {
	if len(obs.Original) == 0 {
		return fmt.Errorf("original split fixture map is empty")
	}
	if !obs.InvalidRejected {
		return fmt.Errorf("late invalid JSONL input was not rejected")
	}
	if !obs.OverlapRejected {
		return fmt.Errorf("cross-split group overlap input was not rejected")
	}
	if err := requireExactFileMap("after invalid JSONL", obs.Original, obs.AfterInvalid); err != nil {
		return err
	}
	if err := requireExactFileMap("after cross-split overlap", obs.Original, obs.AfterOverlap); err != nil {
		return err
	}
	if len(obs.ExpectedValid) == 0 {
		return fmt.Errorf("expected valid split output map is empty")
	}
	if err := requireExactFileMap("valid run 1", obs.ExpectedValid, obs.ValidRun1); err != nil {
		return err
	}
	if err := requireExactFileMap("valid run 2", obs.ExpectedValid, obs.ValidRun2); err != nil {
		return err
	}
	return nil
}

// UsageLedgerEntry represents one usage accounting observation.
type UsageLedgerEntry struct {
	CaseKind     string // "absent_opt_in", "empty_choices", "ordinary"
	RequestID    string
	PromptTokens int
	FinishReason *string
	Interpreter  string // "portable_python" or "project_runner"
}

// GradeUsageLedger verifies usage ledger invariants and interpreter provenance recording.
func GradeUsageLedger(entries []UsageLedgerEntry) error {
	seen := make(map[string]bool)
	for _, e := range entries {
		if e.Interpreter == "" {
			return fmt.Errorf("usage entry %s missing interpreter provenance", e.CaseKind)
		}
		if e.RequestID == "" {
			return fmt.Errorf("usage entry %s missing request ID", e.CaseKind)
		}
		switch e.CaseKind {
		case "empty_choices":
			if e.FinishReason != nil {
				return fmt.Errorf("empty_choices must have null finish_reason, got %q", *e.FinishReason)
			}
		case "ordinary":
			if e.FinishReason == nil || *e.FinishReason == "" {
				return fmt.Errorf("ordinary entry must have non-empty finish_reason")
			}
			if e.PromptTokens <= 0 {
				return fmt.Errorf("ordinary entry must record positive usage tokens")
			}
		case "absent_opt_in":
		default:
			return fmt.Errorf("unknown usage case kind %q", e.CaseKind)
		}
		seen[e.CaseKind] = true
	}
	for _, req := range []string{"absent_opt_in", "empty_choices", "ordinary"} {
		if !seen[req] {
			return fmt.Errorf("missing required usage ledger case %q", req)
		}
	}
	return nil
}
