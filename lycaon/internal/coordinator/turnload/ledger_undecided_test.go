package turnload

import "testing"

func TestLedgerUndecidedTurnKeepsOnlyRequestedLoads(t *testing.T) {
	l := NewLedger()
	all := []string{"command", "edit", "web_search"}
	l.BeginTurn("s1", opening("u1", Boundary{Cache: CacheCold, Reason: ColdFirstTurn}, all...), omit(predict("edit", "command"), "unit:tests"))
	l.Activate("s1", []string{"web_search"}, "look it up")

	l.BeginUndecidedTurn("s1", TurnOpening{OpeningMessageID: "u2", Loadable: all, HistoryEpoch: "e2"})
	if got := standingNames(l, "s1"); got != "web_search" {
		t.Fatalf("undecided standing = %s, want only the requested tool", got)
	}
	if omitted := l.Omitted("s1"); len(omitted) != 0 {
		t.Fatalf("undecided turn omits %v, want every unit rendered", omitted)
	}
	if l.TurnMessageID("s1") != "u2" || l.Standing("s1").HistoryEpoch != "e2" {
		t.Fatalf("undecided turn opening %q, epoch %q", l.TurnMessageID("s1"), l.Standing("s1").HistoryEpoch)
	}
}

func TestLedgerUndecidedTurnDropsRequestedToolsTheSurfaceCannotLoad(t *testing.T) {
	l := NewLedger()
	l.BeginUndecidedTurn("s1", TurnOpening{OpeningMessageID: "u1", Loadable: []string{"web_search"}})
	l.Activate("s1", []string{"web_search"}, "look it up")
	l.BeginUndecidedTurn("s1", TurnOpening{OpeningMessageID: "u2", Loadable: []string{"command"}})
	if got := standingNames(l, "s1"); got != "" {
		t.Fatalf("standing = %q, want nothing the surface cannot load", got)
	}
}

func TestLedgerUndecidedWarmTurnRetainsPredictionsUntilCold(t *testing.T) {
	l := NewLedger()
	l.BeginTurn("s", opening("u1", cold, "edit", "command"), predict("edit"))
	l.Activate("s", []string{"command"}, "run checks")
	l.BeginUndecidedTurn("s", opening("u2", warm, "edit", "command"))
	if got := standingNames(l, "s"); got != "command,edit" {
		t.Fatalf("warm turn dropped tools: %s", got)
	}
	l.BeginUndecidedTurn("s", opening("u3", cold, "edit", "command"))
	if got := standingNames(l, "s"); got != "command" {
		t.Fatalf("cold turn kept obsolete prediction: %s", got)
	}
}
