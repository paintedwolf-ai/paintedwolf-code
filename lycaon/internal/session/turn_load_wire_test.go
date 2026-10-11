package session

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func decisionsJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	testutil.FailErr(t, "marshal decisions", err)
	return string(raw)
}

func TestTurnLoadWireProjectsATurnDecision(t *testing.T) {
	engine := decide.Engine{Name: "Bialy", Model: "mmbert-base", Head: "turn-load"}
	decision := turnload.Decision{
		Kind: "change", KindConfidence: 0.86,
		Tools:   map[string]float64{"git_commit": 0.88, "edit": 0.62},
		Omitted: map[string]turnload.Verdict{"web-research": {P: 0.06, Confidence: 0.94}},
		Candidates: []turnload.Candidate{
			{Kind: turnload.KindTool, ID: "git_commit"}, {Kind: turnload.KindTool, ID: "edit"},
			{Kind: turnload.KindGuide, ID: "web-research"}, {Kind: turnload.KindGuide, ID: "git-consent"}, {Kind: turnload.KindGuide, ID: "edit-ladder"},
		},
		Engine: engine,
	}
	boundary := turnload.Boundary{Cache: turnload.CacheWarm, IdleMS: 95_000, ColdAfterMS: 360_000}
	standing := turnload.Standing{
		Tools: []turnload.Load{
			{Tool: "git_commit", Source: turnload.SourcePredicted, P: 0.88, Turn: "user-1"},
			{Tool: "edit", Source: turnload.SourcePredicted, P: 0.62, Turn: "user-0"},
			{Tool: "web_search", Source: turnload.SourceRequested, Need: "look it up", Turn: "user-0"},
		},
		Omitted: []string{"web-research"},
	}
	receipt := store.TurnLoadReceipt{
		ID: 7, SessionID: "s1", Trigger: store.TurnLoadTriggerTurn, OpeningMessageID: "user-1",
		Engine: "Bialy/mmbert-base#turn-load", Decisions: decisionsJSON(t, map[string]any{"turn": decision, "floor": []string{"read", "grep"}, "boundary": boundary}),
		Standing: decisionsJSON(t, standing), ElapsedMs: 438,
	}
	got := transcript.TurnLoadWire(receipt)
	if len(got.Floor) != 2 || got.Floor[0] != "grep" || got.Floor[1] != "read" {
		t.Fatalf("floor = %v, want the surface's floor sorted", got.Floor)
	}
	if got.Trigger != api.TurnLoadTriggerTurn || got.OpeningMessageID != "user-1" || got.ToolCallID != "" || got.Abstained {
		t.Fatalf("identity = %+v", got)
	}
	if got.Engine == nil || got.Engine.Name != "Bialy" || got.Engine.Head != "turn-load" || got.Engine.Label != "Bialy/mmbert-base#turn-load" {
		t.Fatalf("engine = %+v", got.Engine)
	}
	if got.Kind == nil || got.Kind.Value != "change" || got.Kind.Confidence != 0.86 {
		t.Fatalf("kind = %+v", got.Kind)
	}
	wantTools := []api.TurnLoadTool{
		{Tool: "edit", Source: api.TurnLoadToolSourcePredicted, P: 0.62, Carried: true},
		{Tool: "git_commit", Source: api.TurnLoadToolSourcePredicted, P: 0.88},
		{Tool: "web_search", Source: api.TurnLoadToolSourceRequested, Need: "look it up", Carried: true},
	}
	if len(got.Tools) != len(wantTools) {
		t.Fatalf("tools = %+v, want the standing set", got.Tools)
	}
	for i, want := range wantTools {
		if got.Tools[i] != want {
			t.Fatalf("tool %d = %+v, want %+v", i, got.Tools[i], want)
		}
	}
	if got.Boundary == nil || got.Boundary.Cache != api.TurnLoadCacheStateWarm || got.Boundary.IdleMs != 95_000 || got.Boundary.ColdAfterMs != 360_000 {
		t.Fatalf("boundary = %+v", got.Boundary)
	}
	if got.Guides == nil || got.Guides.Rendered != 2 || got.Guides.Omitted != 1 {
		t.Fatalf("guides = %+v, want 2 rendered of 3 with 1 omitted", got.Guides)
	}
	if got.Match != nil || got.ElapsedMs != 438 {
		t.Fatalf("turn receipt carries a match or lost its timing: %+v", got)
	}
}

func TestTurnLoadWireAbstainedTurnCarriesNoEngineOrGuides(t *testing.T) {
	decision := turnload.Decision{Abstained: true, Reason: "engine unavailable", Candidates: []turnload.Candidate{{Kind: turnload.KindGuide, ID: "web-research"}}}
	receipt := store.TurnLoadReceipt{
		SessionID: "s1", Trigger: store.TurnLoadTriggerTurn, OpeningMessageID: "user-1", Abstained: true, Reason: "engine unavailable",
		Decisions: decisionsJSON(t, map[string]any{"turn": decision}),
	}
	got := transcript.TurnLoadWire(receipt)
	if !got.Abstained || got.Reason != "engine unavailable" || got.Engine != nil || got.Guides != nil || got.Kind != nil {
		t.Fatalf("abstained projection = %+v", got)
	}
	if got.Tools == nil || len(got.Tools) != 0 || got.Floor == nil {
		t.Fatalf("abstained lists must be empty, not absent: %+v", got)
	}
}

func TestTurnLoadWireProjectsRequestAndLookupMatches(t *testing.T) {
	engine := decide.Engine{Name: "Bialy", Model: "mmbert-base", Head: "turn-load"}
	request := turnload.RequestOutcome{Need: "git tools to commit", Exact: []string{"git_stash"}, Ranked: map[string]float64{"git_commit": 3.2, "git_add": 2.9}, Engine: engine}
	got := transcript.TurnLoadWire(store.TurnLoadReceipt{
		SessionID: "s1", Trigger: store.TurnLoadTriggerRequest, OpeningMessageID: "user-1", ToolCallID: "call-9",
		Engine: "Bialy/mmbert-base#turn-load", Decisions: decisionsJSON(t, map[string]any{"request": request}), ElapsedMs: 310,
	})
	if got.Trigger != api.TurnLoadTriggerRequest || got.ToolCallID != "call-9" || got.Match == nil {
		t.Fatalf("request projection = %+v", got)
	}
	if got.Match.By != api.TurnLoadMatchByEngine || got.Match.Need != "git tools to commit" {
		t.Fatalf("request match = %+v", got.Match)
	}
	if names := got.Match.Names; len(names) != 3 || names[0] != "git_stash" || names[1] != "git_commit" || names[2] != "git_add" {
		t.Fatalf("request names = %v, want exact then ranked best first", names)
	}

	lookup := turnload.LookupOutcome{Need: "release notes", Abstained: true, Reason: "engine unavailable"}
	got = transcript.TurnLoadWire(store.TurnLoadReceipt{
		SessionID: "s1", Trigger: store.TurnLoadTriggerLookup, ToolCallID: "call-10", Abstained: true, Reason: "engine unavailable",
		Decisions: decisionsJSON(t, map[string]any{"lookup": lookup}),
	})
	if got.Match == nil || got.Match.By != api.TurnLoadMatchByNone || len(got.Match.Names) != 0 || got.Engine != nil {
		t.Fatalf("lookup projection = %+v match=%+v", got, got.Match)
	}

	got = transcript.TurnLoadWire(store.TurnLoadReceipt{
		SessionID: "s1", Trigger: store.TurnLoadTriggerRequest, ToolCallID: "call-11", Abstained: true, Reason: "nothing to rank",
		Decisions: decisionsJSON(t, map[string]any{"request": turnload.RequestOutcome{Need: "", Abstained: true, Reason: "nothing to rank"}}),
	})
	if got.Match == nil || got.Match.By != api.TurnLoadMatchByNone || got.Match.Names == nil {
		t.Fatalf("empty request must still carry a match with an empty list: %+v", got.Match)
	}
}
