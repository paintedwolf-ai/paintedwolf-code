package turnload

import (
	"context"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"reflect"
	"testing"
)

func TestFallbackVisibilityPreservesRequestProvenance(t *testing.T) {
	ledger := NewLedger()
	opening := TurnOpening{OpeningMessageID: "turn", Loadable: []string{"task", "search"}}
	ledger.BeginTurn("s", opening, Decision{Failure: RankingUnavailable, Abstained: true})
	if got := ledger.Active("s"); !reflect.DeepEqual(got, map[string]bool{"task": true, "search": true}) {
		t.Fatalf("active tools = %v", got)
	}
	if !ledger.FullCatalog("s") || len(ledger.Needed("s")) != 0 {
		t.Fatal("fallback invented demand or hid the catalog")
	}
	for _, load := range ledger.Standing("s").Tools {
		if load.Source != SourceFallback || load.P != 0 || load.Need != "" {
			t.Fatalf("fallback provenance = %+v", load)
		}
	}
	ledger.Activate("s", []string{"task"}, "delegate")
	restored := NewLedger()
	restored.Restore("s", ledger.Standing("s"), nil)
	if !restored.FullCatalog("s") || !reflect.DeepEqual(restored.Needed("s"), []string{"task"}) {
		t.Fatal("restart lost visibility or request provenance")
	}
	// A warm boundary preserves visibility but applies the current permitted set.
	opening.Loadable = []string{"task", "read"}
	opening.Boundary = Boundary{Cache: CacheWarm}
	restored.BeginTurn("s", opening, Decision{})
	if !restored.FullCatalog("s") || !reflect.DeepEqual(restored.Active("s"), map[string]bool{"task": true, "read": true}) {
		t.Fatalf("warm surface = %+v", restored.Standing("s"))
	}
	opening.Boundary = Boundary{Cache: CacheCold}
	restored.BeginTurn("s", opening, Decision{Abstained: true})
	if !restored.FullCatalog("s") {
		t.Fatal("an undecided cold turn hid the catalog")
	}
	restored.BeginTurn("s", opening, Decision{})
	if restored.FullCatalog("s") || !reflect.DeepEqual(restored.Active("s"), map[string]bool{"task": true}) {
		t.Fatalf("healthy cold surface = %+v", restored.Standing("s"))
	}
}

func TestMidTurnSelectionFailureExpandsOnlyPermittedTools(t *testing.T) {
	for _, failure := range []RankingFailure{RankingUnavailable, RankingDisabled, RankingTimeout, RankingFault, RankingCanceled, ""} {
		t.Run(string(failure), func(t *testing.T) {
			ledger := NewLedger()
			ledger.BeginTurn("s", TurnOpening{Loadable: []string{"task", "read"}}, Decision{Tools: map[string]float64{"read": .9}})
			ledger.ExposeCatalog("s", failure)
			if ledger.FullCatalog("s") != failure.RequiresFullCatalog() {
				t.Fatalf("catalog state for %q", failure)
			}
			if ledger.Active("s")["task"] != failure.RequiresFullCatalog() {
				t.Fatalf("tool state for %q", failure)
			}
		})
	}
}

func TestContinuationReconcilesRestoredVisibility(t *testing.T) {
	ledger := NewLedger()
	ledger.Restore("s", Standing{Tools: []Load{{Tool: "removed", Source: SourcePredicted}}}, nil)
	ledger.ReconcileCatalog("s", []string{"task"}, RankingUnavailable)
	if !ledger.FullCatalog("s") || !reflect.DeepEqual(ledger.Active("s"), map[string]bool{"task": true}) {
		t.Fatalf("continuation = %+v", ledger.Standing("s"))
	}
	ledger.BeginUndecidedTurn("s", TurnOpening{Loadable: []string{"task", "search"}, Boundary: Boundary{Cache: CacheCold}})
	if !ledger.FullCatalog("s") || len(ledger.Active("s")) != 2 {
		t.Fatalf("undecided = %+v", ledger.Standing("s"))
	}
}

func TestSelectionFailureIsTypedWithoutMatchingReason(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want RankingFailure
	}{{decide.ErrDisabled, RankingDisabled}, {decide.ErrUnavailable, RankingUnavailable}, {decide.ErrDeadline, RankingTimeout}, {context.Canceled, RankingCanceled}} {
		got := Decide(t.Context(), &decidetest.Fake{Err: tc.err}, testSpec(), State{}, fixtureCandidates())
		if got.Failure != tc.want {
			t.Fatalf("failure = %q, want %q", got.Failure, tc.want)
		}
	}
	if got := Decide(t.Context(), &decidetest.Fake{}, testSpec(), State{}, nil); got.Failure != "" {
		t.Fatalf("empty candidate set became a failure: %+v", got)
	}
}
