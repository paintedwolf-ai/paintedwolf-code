package turnload

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	warm = Boundary{Cache: CacheWarm}
	cold = Boundary{Cache: CacheCold, Reason: ColdIdle}
)

func opening(id string, boundary Boundary, loadable ...string) TurnOpening {
	return TurnOpening{OpeningMessageID: id, Loadable: loadable, Boundary: boundary}
}

func predict(tools ...string) Decision {
	d := Decision{Tools: map[string]float64{}}
	for _, tool := range tools {
		d.Tools[tool] = 0.8
	}
	return d
}

func standingNames(l *Ledger, session string) string {
	var names []string
	for _, load := range l.Standing(session).Tools {
		names = append(names, load.Tool)
	}
	return strings.Join(names, ",")
}

func TestLedgerKeepsTheStandingSetAcrossWarmTurns(t *testing.T) {
	l := NewLedger()
	all := []string{"command", "edit", "git_diff", "web_search"}
	l.BeginTurn("s1", opening("u1", Boundary{Cache: CacheCold, Reason: ColdFirstTurn}, all...), predict("edit", "command"))
	l.Activate("s1", []string{"web_search"}, "look it up")
	l.Use("s1", "read")
	if got := standingNames(l, "s1"); got != "command,edit,web_search" {
		t.Fatalf("first turn standing = %s", got)
	}
	// A warm turn keeps every standing tool and adds its own predictions.
	l.BeginTurn("s1", opening("u2", warm, all...), predict("git_diff"))
	if got := standingNames(l, "s1"); got != "command,edit,git_diff,web_search" {
		t.Fatalf("warm turn standing = %s", got)
	}
	for _, load := range l.Standing("s1").Tools {
		switch load.Tool {
		case "edit":
			if load.Turn != "u1" || load.Source != SourcePredicted {
				t.Fatalf("carried prediction lost its origin: %+v", load)
			}
		case "git_diff":
			if load.Turn != "u2" {
				t.Fatalf("new prediction joined on the wrong turn: %+v", load)
			}
		case "web_search":
			if load.Source != SourceRequested || load.Need != "look it up" || load.Turn != "u1" {
				t.Fatalf("request lost: %+v", load)
			}
		}
	}
	// A cold turn replaces predicted tools while keeping tools the chat requested.
	l.BeginTurn("s1", opening("u3", cold, all...), predict("git_diff"))
	if got := standingNames(l, "s1"); got != "git_diff,web_search" {
		t.Fatalf("cold turn standing = %s", got)
	}
	// An abstained decision keeps the standing set even when cold.
	l.BeginTurn("s1", opening("u4", cold, all...), Decision{Abstained: true})
	if got := standingNames(l, "s1"); got != "git_diff,web_search" {
		t.Fatalf("abstained cold turn standing = %s", got)
	}
	// A tool the surface no longer offers leaves.
	l.BeginTurn("s1", opening("u5", warm, "command"), predict())
	if got := standingNames(l, "s1"); got != "" {
		t.Fatalf("unloadable tools stayed: %s", got)
	}
	if got := strings.Join(l.Needed("s1"), ","); got != "read,web_search" {
		t.Fatalf("needed = %s", got)
	}

	l.Forget("s1")
	if l.Active("s1") != nil {
		t.Fatal("forget must drop the session")
	}
}

func TestLedgerBeginTurnKeepsRequestedToolsAcrossColdIdleBoundaries(t *testing.T) {
	l := NewLedger()
	all := []string{"command", "command_output", "command_stop", "edit", "git_commit"}
	l.BeginTurn("s1", opening("u1", Boundary{Cache: CacheCold, Reason: ColdFirstTurn}, all...), predict("command", "edit"))
	// Explicitly requesting an already-predicted tool upgrades its standing source to SourceRequested,
	// and requesting a deferred tool adds it as SourceRequested.
	l.Activate("s1", []string{"edit", "git_commit"}, "commit the edited verification-plan.json on the branch")

	// After a long wait lease expires the prompt cache (ColdIdle), predicted and companion
	// tools are replaced by the new decision while requested tools remain standing.
	l.BeginTurn("s1", opening("u2", Boundary{Cache: CacheCold, Reason: ColdIdle}, all...), predict())
	if got := standingNames(l, "s1"); got != "edit,git_commit" {
		t.Fatalf("cold idle standing = %s, want edit,git_commit", got)
	}
	for _, load := range l.Standing("s1").Tools {
		if load.Source != SourceRequested || load.Turn != "u1" {
			t.Fatalf("unexpected standing load after cold idle: %+v", load)
		}
	}
}

func TestRestoreBringsBackTheRecordedStandingSurface(t *testing.T) {
	history := []api.Message{
		{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "request_tools"}, {ID: "c2", Name: "git_status"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{
			ToolCallID: "c1", AssistantMessageID: "a1", Tool: "request_tools", Outcome: api.ToolResultOutcomeCompleted,
			Content:    `{"need":"compare branches","loaded":["git_compare"],"already_loaded":["git_status"]}`,
			Invocation: &api.InvocationReceipt{Tool: "request_tools", Owner: "tool_surface", Status: api.InvocationStatusCompleted, Invoked: true},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "c2", AssistantMessageID: "a1", Tool: "git_status", Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "c9", AssistantMessageID: "a9", Tool: "write", Outcome: api.ToolResultOutcomeCompleted}},
	}
	l := NewLedger()
	l.BeginTurn("s1", TurnOpening{OpeningMessageID: "u1", Loadable: []string{"edit", "git_compare"}, Boundary: cold, HistoryEpoch: "1:m9"}, predict("edit"))
	l.Activate("s1", []string{"git_compare"}, "compare branches")
	recorded := l.Standing("s1")

	restarted := NewLedger()
	restarted.Restore("s1", recorded, history)
	snap := restarted.Standing("s1")
	if got := standingNames(restarted, "s1"); got != "edit,git_compare" {
		t.Fatalf("restored standing = %s", got)
	}
	if snap.HistoryEpoch != "1:m9" {
		t.Fatalf("restored epoch %q", snap.HistoryEpoch)
	}
	// History replays what the chat needed; an unmatched result does not.
	if got := strings.Join(restarted.Needed("s1"), ","); got != "git_compare,git_status" {
		t.Fatalf("needed = %s", got)
	}
	// A record in an unknown source spelling is not restored.
	restarted.Restore("s1", Standing{Tools: []Load{{Tool: "legacy", Source: "used"}}}, nil)
	if restarted.Active("s1") != nil {
		t.Fatalf("restored an unknown source: %v", restarted.Active("s1"))
	}
}

func TestReadBoundary(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	base := BoundaryFacts{
		Caches: true, PolicyKnown: true, LastCall: true,
		LastProvider: "fireworks-1", LastModel: "glm", NextProvider: "fireworks-1", NextModel: "glm",
		LastStarted: now.Add(-2 * time.Minute), Now: now, ColdAfter: 6 * time.Minute,
	}
	cases := []struct {
		name   string
		edit   func(*BoundaryFacts)
		cache  CacheState
		reason ColdReason
	}{
		{"warm", func(*BoundaryFacts) {}, CacheWarm, ""},
		{"first turn", func(f *BoundaryFacts) { f.FirstTurn = true }, CacheCold, ColdFirstTurn},
		{"missing call facts preserve standing", func(f *BoundaryFacts) { f.LastCall = false }, CacheWarm, ""},
		{"missing policy preserves standing", func(f *BoundaryFacts) { f.PolicyKnown, f.Caches = false, false }, CacheWarm, ""},
		{"uncached route", func(f *BoundaryFacts) { f.Caches = false }, CacheCold, ColdUncached},
		{"model changed", func(f *BoundaryFacts) { f.NextModel = "kimi" }, CacheCold, ColdModelChanged},
		{"provider changed", func(f *BoundaryFacts) { f.NextProvider = "anthropic-1" }, CacheCold, ColdModelChanged},
		{"unloaded runner", func(f *BoundaryFacts) { f.ResidencyKnown, f.Resident = true, false }, CacheCold, ColdNotResident},
		{"unknown residency stays warm", func(f *BoundaryFacts) { f.ResidencyKnown = false }, CacheWarm, ""},
		{"idle past the lifetime", func(f *BoundaryFacts) { f.LastStarted = now.Add(-6 * time.Minute) }, CacheCold, ColdIdle},
		{"idle with no known lifetime", func(f *BoundaryFacts) { f.LastStarted, f.ColdAfter = now.Add(-3*time.Hour), 0 }, CacheWarm, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			facts := base
			tc.edit(&facts)
			got := ReadBoundary(facts)
			if got.Cache != tc.cache || got.Reason != tc.reason {
				t.Fatalf("boundary = %+v, want %s %s", got, tc.cache, tc.reason)
			}
		})
	}
	if got := ReadBoundary(base); got.IdleMS != (2*time.Minute).Milliseconds() || got.ColdAfterMS != (6*time.Minute).Milliseconds() {
		t.Fatalf("boundary timing = %+v", got)
	}
}

func TestCompactionPreservesStandingSurfaceAcrossRestart(t *testing.T) {
	ledger := NewLedger()
	first := opening("u1", cold, "edit", "git_compare")
	first.HistoryEpoch = "1:old"
	ledger.BeginTurn("session", first, predict("edit"))
	restored := NewLedger()
	restored.Restore("session", ledger.Standing("session"), nil)
	now := time.Now()
	boundary := ReadBoundary(BoundaryFacts{
		Caches: true, PolicyKnown: true, LastCall: true, LastProvider: "p", NextProvider: "p", LastModel: "m", NextModel: "m",
		LastStarted: now.Add(-time.Minute), Now: now, ColdAfter: time.Hour,
	})
	next := opening("u2", boundary, "edit", "git_compare")
	next.HistoryEpoch = "2:new"
	restored.BeginTurn("session", next, predict("git_compare"))
	if got := standingNames(restored, "session"); got != "edit,git_compare" {
		t.Fatalf("compaction dropped standing tools: %s", got)
	}
	if restored.Standing("session").HistoryEpoch != "2:new" {
		t.Fatal("receipt lost history provenance")
	}
}

func TestLedgerNeverPromotesPredictionOutsideLoadableSurface(t *testing.T) {
	l := NewLedger()
	l.BeginTurn("session", opening("u1", cold, "read"), predict("read", "command"))
	if got := standingNames(l, "session"); got != "read" {
		t.Fatalf("off-surface prediction loaded: %s", got)
	}
}

func TestRestoreReadsAStandingRecordThatCarriesOmittedUnits(t *testing.T) {
	var recorded Standing
	raw := `{"tools":[{"tool":"edit","source":"predicted","p":0.8,"turn":"u1"}],"omitted":["native-recall-tool"],"history_epoch":"1:m9"}`
	testutil.FailErr(t, "decode standing", json.Unmarshal([]byte(raw), &recorded))
	l := NewLedger()
	l.Restore("s1", recorded, nil)
	if got := standingNames(l, "s1"); got != "edit" {
		t.Fatalf("restored standing = %s", got)
	}
	encoded, err := json.Marshal(l.Standing("s1"))
	testutil.FailErr(t, "encode standing", err)
	if strings.Contains(string(encoded), "omitted") {
		t.Fatalf("restored standing kept the omitted units: %s", encoded)
	}
}
