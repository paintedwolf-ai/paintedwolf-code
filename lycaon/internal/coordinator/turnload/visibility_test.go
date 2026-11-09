package turnload

import (
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
