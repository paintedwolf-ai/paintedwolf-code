package session

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTurnReceiptRecordsTheOfferedCandidates(t *testing.T) {
	t.Parallel()
	d := &turnDecision{
		state:    turnload.State{Host: "worker", Surface: "implement", User: "fix it"},
		floor:    []string{"read"},
		loadable: []string{"jq_edit", "web_search"},
		guides:   []string{"style"},
		decision: turnload.Decision{Abstained: true, Reason: "engine disabled"},
	}
	receipt := d.receipt()
	var decisions struct {
		Floor      []string            `json:"floor"`
		Candidates map[string][]string `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(receipt.Decisions), &decisions); err != nil {
		testutil.FailErr(t, "decode decisions", err)
	}
	if !slices.Equal(decisions.Candidates["loadable"], d.loadable) || !slices.Equal(decisions.Candidates["guides"], d.guides) {
		t.Fatalf("candidates = %v, want loadable %v and guides %v", decisions.Candidates, d.loadable, d.guides)
	}
	if !slices.Equal(decisions.Floor, d.floor) || receipt.SurfaceID != "implement" || !receipt.Abstained {
		t.Fatalf("receipt = %+v", receipt)
	}
	empty := (&turnDecision{}).receipt()
	if !strings.Contains(empty.Decisions, `"candidates":{"guides":[],"loadable":[]}`) {
		t.Fatalf("an empty decision must still record empty candidate lists: %s", empty.Decisions)
	}
}

func TestRecentToolNamesNewestFirstAndDistinct(t *testing.T) {
	t.Parallel()
	history := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "read"}, {Name: "grep"}}},
		{Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "next"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "grep"}, {Name: "write"}}},
	}
	got := recentToolNames(history, 10)
	want := []string{"write", "grep", "read"}
	if len(got) != len(want) {
		t.Fatalf("recent = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recent = %v want %v", got, want)
		}
	}
	if limited := recentToolNames(history, 1); len(limited) != 1 || limited[0] != "write" {
		t.Fatalf("limited = %v", limited)
	}
}

func TestAttachmentKindsNameMediaMajorTypes(t *testing.T) {
	t.Parallel()
	in := PromptInput{
		ArtifactIDs: []string{"a"},
		ContentParts: []api.MessageContentPart{
			{MediaType: "image/png"},
			{MediaType: "text/markdown"},
			{MediaType: "image/jpeg"},
			{Source: "search_hit"},
		},
	}
	got := attachmentKinds(in)
	want := []string{"artifact", "image", "text", "search_hit"}
	if len(got) != len(want) {
		t.Fatalf("kinds = %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kinds = %v want %v", got, want)
		}
	}
}

func TestEngineLabelPrefersTheFirstNamedEngine(t *testing.T) {
	t.Parallel()
	if got := engineLabel(decide.Engine{}, decide.Engine{Name: "Bialy", Model: "m", Head: "turn-load"}); got != "Bialy/m#turn-load" {
		t.Fatalf("label = %q", got)
	}
	if got := engineLabel(decide.Engine{}); got != "" {
		t.Fatalf("empty label = %q", got)
	}
}

type activityEdges struct{ got []api.ActivityEvent }

func (a *activityEdges) ObserveActivity(ev api.ActivityEvent) { a.got = append(a.got, ev) }

func TestDecidingLeaseBracketsAnAvailableEngine(t *testing.T) {
	t.Parallel()
	edges := &activityEdges{}
	m := &Manager{events: &events.Publisher{ActivityObserver: edges}, decider: &decidetest.Fake{}}
	sess := &api.Session{ID: "s1", ProjectID: "p1"}
	finish := m.beginDeciding(context.Background(), sess, "s1", api.TurnLoadTriggerRequest, "call-1")
	finish()
	finish()
	if len(edges.got) != 2 {
		t.Fatalf("edges = %+v, want one active and one done", edges.got)
	}
	open, done := edges.got[0], edges.got[1]
	if open.Kind != api.ActivityKindDeciding || open.Status != api.ActivityStatusActive || done.Status != api.ActivityStatusDone {
		t.Fatalf("edges = %+v", edges.got)
	}
	if open.ActivityID != done.ActivityID || open.DecisionTrigger != api.TurnLoadTriggerRequest || open.ToolCallID != "call-1" {
		t.Fatalf("lease = %+v", open)
	}
}

func TestDecidingLeaseAbsentWithoutAnEngine(t *testing.T) {
	t.Parallel()
	edges := &activityEdges{}
	m := &Manager{events: &events.Publisher{ActivityObserver: edges}}
	m.beginDeciding(context.Background(), &api.Session{ID: "s1"}, "s1", api.TurnLoadTriggerTurn, "")()
	m.decider = &decidetest.Fake{Unavailable: true}
	m.beginDeciding(context.Background(), &api.Session{ID: "s1"}, "s1", api.TurnLoadTriggerTurn, "")()
	if len(edges.got) != 0 {
		t.Fatalf("an unavailable engine opened a lease: %+v", edges.got)
	}
}

func TestManagerWithoutLedgerOrDeciderAbstains(t *testing.T) {
	t.Parallel()
	var m *Manager
	if m.LoadedTools("s") != nil {
		t.Fatal("nil manager must report nothing loaded")
	}
	if (&Manager{}).turnDecider().Available() {
		t.Fatal("an unwired decider must be absent")
	}
}
