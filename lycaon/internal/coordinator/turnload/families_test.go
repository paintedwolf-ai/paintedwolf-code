package turnload

import (
	"testing"
)

func TestPredictedFamiliesFollowStandingLifetime(t *testing.T) {
	for _, selected := range []string{"web_search", "fetch_url", "page_open", "terminal_open"} {
		t.Run(selected, func(t *testing.T) {
			all := []string{"web_search", "fetch_url", "page_open", "page_act", "terminal_open", "terminal_read", "command"}
			l := NewLedger()
			l.BeginTurn("s", opening("u1", cold, all...), predict(selected))
			if len(l.Active("s")) != 2 || !l.Active("s")[selected] {
				t.Fatalf("family not loaded: %+v", l.Standing("s"))
			}
			for _, load := range l.Standing("s").Tools {
				if load.Tool != selected && (load.Source != SourceCompanion || load.With != selected || load.P != 0) {
					t.Fatalf("family provenance = %+v", load)
				}
			}
			restored := NewLedger()
			restored.Restore("s", l.Standing("s"), nil)
			restored.BeginTurn("s", opening("u2", warm, all...), predict())
			if standingNames(restored, "s") != standingNames(l, "s") {
				t.Fatal("warm restart dropped family")
			}
			restored.BeginTurn("s", opening("u3", cold, all...), predict())
			if len(restored.Active("s")) != 0 {
				t.Fatal("cold prediction retained family")
			}
		})
	}
}

func TestFamilyLoadingKeepsUnavailableMembersOut(t *testing.T) {
	l := NewLedger()
	l.BeginTurn("s", opening("u1", cold, "web_search"), predict("web_search", "page_open"))
	if got := standingNames(l, "s"); got != "web_search" {
		t.Fatalf("restricted load = %s", got)
	}
	l.BeginTurn("s", opening("u2", warm, "fetch_url"), predict())
	if len(l.Active("s")) != 0 {
		t.Fatal("unavailable seed loaded a family member")
	}
}

func TestRequestedFamilySurvivesColdRestartAndUndecidedTurns(t *testing.T) {
	l := NewLedger()
	l.Activate("s", []string{"web_search"}, "research")
	restored := NewLedger()
	restored.Restore("s", l.Standing("s"), nil)
	restored.BeginUndecidedTurn("s", opening("u1", cold, "web_search", "fetch_url"))
	if got := standingNames(restored, "s"); got != "fetch_url,web_search" {
		t.Fatalf("requested family = %s", got)
	}
	for _, load := range restored.Standing("s").Tools {
		if load.Source != SourceRequested || load.Need != "research" {
			t.Fatalf("requested family provenance = %+v", load)
		}
	}
	restored.BeginTurn("s", opening("u2", cold, "web_search"), predict())
	if got := standingNames(restored, "s"); got != "web_search" {
		t.Fatalf("revoked family member persisted: %s", got)
	}
}
