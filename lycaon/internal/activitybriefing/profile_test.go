package activitybriefing

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/briefing"
	"github.com/lycaon/lycaon/internal/briefingstore"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const project = testdbseed.DefaultProjectID

type sessions struct{ projectID string }

func (s sessions) Get(context.Context, string) (*wire.Session, error) {
	return &wire.Session{ProjectID: s.projectID}, nil
}

// programs serves one activity's page; siblings are the program's other
// activities, listed on the page as the recorder lists them.
type programs struct {
	activity wire.CommandActivity
	siblings int
}

func (p *programs) ReadProgramActivityPage(context.Context, string, string, string, string, int) (wire.CommandProgramReceiptPage, error) {
	exit := 0
	whole := []wire.CommandActivity{p.activity}
	for range p.siblings {
		whole = append(whole, wire.CommandActivity{ID: "sibling"})
	}
	return wire.CommandProgramReceiptPage{Activities: whole,
		Executions: []wire.CommandExecutionReceipt{{Command: "go test ./...", State: p.activity.State, StatusCode: &exit,
			Outputs: []wire.CommandExecutionOutput{{Stream: "stdout", Preview: "ok  pkg", TailPreview: "ok  pkg\nPASS"}}}}}, nil
}

type cause struct{ read briefing.CausalRead }

func (c cause) ReadActionCause(context.Context, briefing.ActionRef) (briefing.CausalRead, error) {
	return c.read, nil
}

func commandTarget() briefing.Target {
	raw, _ := json.Marshal(wire.ToolActivityBriefingTarget{SessionID: "s", ToolCallIds: []string{"call"},
		Members: []wire.ToolActivityBriefingMember{{ProgramID: "prog", ActivityID: "act"}}})
	return briefing.Target{ProjectID: project, Ref: raw}
}

func activity(phase, state string, steps ...string) wire.CommandActivity {
	out := wire.CommandActivity{ID: "act", Phase: phase, State: state}
	for _, step := range steps {
		out.Steps = append(out.Steps, wire.CommandActivityStep{Command: "step", State: step, Count: 1})
	}
	return out
}

func sectionModes(request briefing.Request) map[string]briefing.SectionMode {
	modes := map[string]briefing.SectionMode{}
	for _, section := range request.Sections {
		modes[section.ID] = section.Mode
	}
	return modes
}

// Every opened activity gets a generated purpose; a settled one also gets a
// change, which is generated unless the host refused the activity outright.
func TestGenerationPolicyMatrix(t *testing.T) {
	read := briefing.CausalRead{Found: true, Call: briefing.CallExcerpt{Name: "command", Args: `{"command":"go test ./..."}`}}
	const none = briefing.SectionMode(-1)
	cases := []struct {
		name     string
		activity wire.CommandActivity
		change   briefing.SectionMode
	}{
		{"execute settled", activity("execute", "completed", "completed"), briefing.Generated},
		{"execute running", activity("execute", "running", "running"), none},
		{"failed inspect", activity("inspect", "failed", "failed"), briefing.Generated},
		{"interrupted mutate", activity("mutate", "interrupted", "interrupted"), briefing.Generated},
		{"steps differ", activity("inspect", "completed", "completed", "failed"), briefing.Generated},
		{"inspect completed", activity("inspect", "completed", "completed", "completed"), briefing.Generated},
		{"mutate completed", activity("mutate", "completed", "completed"), briefing.Generated},
		{"inspect running", activity("inspect", "running", "running"), none},
		{"refused", activity("execute", "refused", "refused"), briefing.Deterministic},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			profile := New(sessions{projectID: project}, &programs{activity: c.activity}, cause{read: read})
			request, err := profile.Resolve(t.Context(), commandTarget())
			testutil.FailErr(t, "resolve", err)
			modes := sectionModes(request)
			if modes["purpose"] != briefing.Generated {
				t.Fatalf("purpose mode %v", modes["purpose"])
			}
			change, ok := modes["change"]
			if !ok {
				change = none
			}
			if change != c.change {
				t.Fatalf("change mode %v; want %v", change, c.change)
			}
			if modes["outcome"] != briefing.Deterministic {
				t.Fatalf("outcome mode %v", modes["outcome"])
			}
			for _, section := range request.Sections {
				if section.Mode == briefing.Lookup {
					t.Fatalf("section %s only looks up a stored result", section.ID)
				}
				if section.ID == "change" && section.Mode == briefing.Deterministic && section.Text != nothingRanText {
					t.Fatalf("refused change text %q", section.Text)
				}
			}
		})
	}
	for name, result := range map[string]*briefing.ResultExcerpt{
		"settled tool": {Tool: "read", Outcome: "success", Content: "214 lines", Ref: "message:r"},
		"running tool": nil,
	} {
		tool := New(sessions{projectID: project}, &programs{}, cause{read: briefing.CausalRead{Found: true, Call: briefing.CallExcerpt{Name: "read", Result: result}}})
		raw, _ := json.Marshal(wire.ToolActivityBriefingTarget{SessionID: "s", ToolCallIds: []string{"call"}})
		request, err := tool.Resolve(t.Context(), briefing.Target{ProjectID: project, Ref: raw})
		testutil.FailErr(t, name, err)
		_, hasChange := sectionModes(request)["change"]
		if hasChange != (result != nil) {
			t.Fatalf("%s: change section present = %v", name, hasChange)
		}
	}
}

// Purpose is scoped to the card's steps exactly when the card is part of its
// program; a card that is the whole call explains the call.
func TestPurposeScopesPartOfAProgram(t *testing.T) {
	read := briefing.CausalRead{Found: true, Call: briefing.CallExcerpt{Name: "command", Args: `{"command":"cat > a.go && go test ./..."}`}}
	scope := func(siblings int, steps ...string) string {
		t.Helper()
		profile := New(sessions{projectID: project}, &programs{activity: activity("execute", "completed", steps...), siblings: siblings}, cause{read: read})
		request, err := profile.Resolve(t.Context(), commandTarget())
		testutil.FailErr(t, "resolve", err)
		for _, item := range request.Sections[0].Manifest.Items {
			if item.Key == "phase_scope" {
				return item.Text
			}
		}
		return ""
	}
	if got := scope(1, "completed"); got != "steps: step" {
		t.Fatalf("one step of a two-activity program was not scoped: %q", got)
	}
	if got := scope(0, "completed", "completed"); got != "" {
		t.Fatalf("a card that is its whole call was scoped: %q", got)
	}
}

func TestResolveRefusals(t *testing.T) {
	profile := New(sessions{projectID: project}, &programs{activity: activity("execute", "completed")}, cause{})
	var many []string
	for range maxMembers + 1 {
		many = append(many, "call")
	}
	for name, c := range map[string]struct {
		target wire.ToolActivityBriefingTarget
		want   error
	}{
		"no calls":       {wire.ToolActivityBriefingTarget{SessionID: "s"}, briefing.ErrInvalidTarget},
		"no session":     {wire.ToolActivityBriefingTarget{ToolCallIds: []string{"c"}}, briefing.ErrInvalidTarget},
		"over the cap":   {wire.ToolActivityBriefingTarget{SessionID: "s", ToolCallIds: many}, briefing.ErrTargetUnavailable},
		"unknown member": {wire.ToolActivityBriefingTarget{SessionID: "s", ToolCallIds: []string{"c"}, Members: []wire.ToolActivityBriefingMember{{ProgramID: "p", ActivityID: "missing"}}}, briefing.ErrTargetUnavailable},
	} {
		raw, _ := json.Marshal(c.target)
		if _, err := profile.Resolve(t.Context(), briefing.Target{ProjectID: project, Ref: raw}); !errors.Is(err, c.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	other := New(sessions{projectID: "another"}, &programs{activity: activity("execute", "completed")}, cause{})
	if _, err := other.Resolve(t.Context(), commandTarget()); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("another project's session: %v", err)
	}
	if _, err := profile.Resolve(t.Context(), commandTarget()); !errors.Is(err, briefing.ErrTargetUnavailable) {
		t.Fatalf("a call missing from the transcript was summarized: %v", err)
	}
	if _, err := profile.Resolve(t.Context(), briefing.Target{ProjectID: project, Ref: json.RawMessage(`{"session_id":"s","tool_call_ids":["c"],"extra":1}`)}); !errors.Is(err, briefing.ErrInvalidTarget) {
		t.Fatalf("unknown field: %v", err)
	}
}

type countingText struct{ calls atomic.Int64 }

func (c *countingText) Generate(context.Context, briefing.Invocation) (string, string, string, error) {
	c.calls.Add(1)
	return "Ran the tests; all passed.", "provider", "model", nil
}

// A card opened while running and still open at settlement costs at most
// two calls, and reopening it costs none.
func TestRunningToSettledCostsAtMostTwoCalls(t *testing.T) {
	database := testdbfixture.Open(t, "activity-briefing.db")
	testdbseed.InsertProjectRoot(t, database, project, t.TempDir())
	scheduler := briefing.NewScheduler(context.Background())
	t.Cleanup(func() { scheduler.Stop(); scheduler.Wait(context.Background()) })
	generator := &countingText{}
	engine := briefing.NewEngine(scheduler, briefingstore.NewStore(database, t.TempDir()), generator, nil, nil)
	read := briefing.CausalRead{Found: true, Call: briefing.CallExcerpt{Name: "command", Args: `{"command":"go test ./..."}`}}
	reader := &programs{activity: activity("execute", "running", "running")}
	profile := New(sessions{projectID: project}, reader, cause{read: read})
	open := func() briefing.Snapshot {
		t.Helper()
		request, err := profile.Resolve(t.Context(), commandTarget())
		testutil.FailErr(t, "resolve", err)
		snap, err := engine.Request(t.Context(), request)
		testutil.FailErr(t, "request", err)
		scheduler.Wait(t.Context())
		return snap
	}
	open()
	reader.activity = activity("execute", "completed", "completed")
	open()
	if got := generator.calls.Load(); got != 2 {
		t.Fatalf("running then settled cost %d calls", got)
	}
	if again := open(); again.Status != briefing.StatusComplete || generator.calls.Load() != 2 {
		t.Fatalf("reopen cost a call: %+v, %d calls", again, generator.calls.Load())
	}
}
