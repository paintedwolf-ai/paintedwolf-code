package guidance

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// A producer that stated nothing completed — callers read Resolution rather than
// testing the zero value themselves.
func TestToolResultFactsResolutionIsTotal(t *testing.T) {
	if got := (ToolResultFacts{}).Resolution(); got != api.ToolResultOutcomeCompleted {
		t.Fatalf("zero Resolution = %q want completed", got)
	}
	if !(ToolResultFacts{}).Succeeded() {
		t.Fatal("zero facts must read as succeeded")
	}
	if (ToolResultFacts{Outcome: api.ToolResultOutcomeError}).Succeeded() {
		t.Fatal("a stated error must not read as succeeded")
	}
	if !(ToolResultFacts{Outcome: api.ToolResultOutcomeError}).UnstatedNonSuccess() {
		t.Fatal("error without a code is unstated")
	}
	if (ToolResultFacts{Outcome: api.ToolResultOutcomeError}.WithCode("TOOL_OWNER_FAILED")).UnstatedNonSuccess() {
		t.Fatal("error with a code is stated")
	}
	if (ToolResultFacts{}).UnstatedNonSuccess() {
		t.Fatal("completed facts are not unstated non-success")
	}
}

// Merging is order-independent so producers may state their facts in any order:
// a refusal outranks an error because the call never ran, and either outranks a
// completion.
func TestToolResultFactsOutcomeMergeIsOrderIndependent(t *testing.T) {
	all := []api.ToolResultOutcome{
		api.ToolResultOutcomeCompleted,
		api.ToolResultOutcomeError,
		api.ToolResultOutcomeRejected,
	}
	for _, a := range all {
		for _, b := range all {
			forward := ToolResultFacts{Outcome: a}.WithOutcome(b).Resolution()
			backward := ToolResultFacts{Outcome: b}.WithOutcome(a).Resolution()
			if forward != backward {
				t.Fatalf("merge(%q,%q) = %q but merge(%q,%q) = %q", a, b, forward, b, a, backward)
			}
		}
	}
	got := ToolResultFacts{Outcome: api.ToolResultOutcomeError}.
		WithOutcome(api.ToolResultOutcomeRejected).
		WithOutcome(api.ToolResultOutcomeCompleted).
		Resolution()
	if got != api.ToolResultOutcomeRejected {
		t.Fatalf("resolution = %q want rejected to win", got)
	}
}

func TestToolResultFactsCodesKeepOrderAndDropRepeats(t *testing.T) {
	facts := ToolResultFacts{}.
		WithCode("FIRST").
		WithCode("SECOND").
		WithCode("FIRST").
		WithCode("  ")
	if len(facts.Codes) != 2 {
		t.Fatalf("codes = %v want the two distinct codes", facts.Codes)
	}
	if facts.Codes[0] != "FIRST" || facts.Codes[1] != "SECOND" {
		t.Fatalf("codes = %v want raise order", facts.Codes)
	}
	if !facts.HasCode("SECOND") || facts.HasCode("THIRD") {
		t.Fatal("HasCode disagrees with the raised set")
	}
	if facts.PrimaryCode() != "FIRST" {
		t.Fatalf("primary = %q", facts.PrimaryCode())
	}
}

// Facts pass by value, so a producer cannot reach back into a caller's copy.
func TestToolResultFactsWithCodeDoesNotMutateTheReceiver(t *testing.T) {
	base := ToolResultFacts{}.WithCode("BASE")
	left := base.WithCode("LEFT")
	right := base.WithCode("RIGHT")
	if len(base.Codes) != 1 {
		t.Fatalf("base codes = %v want untouched", base.Codes)
	}
	if left.HasCode("RIGHT") || right.HasCode("LEFT") {
		t.Fatalf("branches share backing storage: left=%v right=%v", left.Codes, right.Codes)
	}
}

func TestToolResultFactsMergeFoldsOutcomeAndCodes(t *testing.T) {
	tool := ToolResultFacts{Outcome: api.ToolResultOutcomeError}.WithCode("SANDBOX_TRY_WRITE_ROOT")
	enriched := ToolResultFacts{}.WithCode("SPEC_POSTURE_PROGRESS")
	merged := tool.Merge(enriched)
	if merged.Resolution() != api.ToolResultOutcomeError {
		t.Fatalf("resolution = %q want the decisive outcome kept", merged.Resolution())
	}
	if len(merged.Codes) != 2 || merged.Codes[0] != "SANDBOX_TRY_WRITE_ROOT" {
		t.Fatalf("codes = %v want both, tool first", merged.Codes)
	}
}

func TestNewRefusalCarriesItsCodeAndFallsBackToIt(t *testing.T) {
	r := NewRefusal("PROGRESS_MISSING", "Rejected: no checklist yet")
	if r.Code() != "PROGRESS_MISSING" {
		t.Fatalf("code = %q", r.Code())
	}
	if r.Facts.Resolution() != api.ToolResultOutcomeRejected {
		t.Fatalf("resolution = %q want rejected", r.Facts.Resolution())
	}
	bare := NewRefusal("PROGRESS_MISSING", "")
	if bare.Body != "PROGRESS_MISSING" {
		t.Fatalf("body = %q want the code as the fallback body", bare.Body)
	}
}

func TestRefusalFromErrorOnlyMatchesRefusals(t *testing.T) {
	if _, ok := RefusalFromError(nil); ok {
		t.Fatal("nil is not a refusal")
	}
	if _, ok := RefusalFromError(errNotARefusal{}); ok {
		t.Fatal("an ordinary failure is not a refusal")
	}
	got, ok := RefusalFromError(NewRefusal("CODE", "body"))
	if !ok || got.Code() != "CODE" {
		t.Fatalf("RefusalFromError did not recover the refusal: %v %v", got, ok)
	}
}

type errNotARefusal struct{}

func (errNotARefusal) Error() string { return "dial tcp: connection refused" }
