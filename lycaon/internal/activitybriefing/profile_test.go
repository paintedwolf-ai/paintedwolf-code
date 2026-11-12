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

type programs struct{ activity wire.CommandActivity }

func (p *programs) ReadProgramActivityPage(context.Context, string, string, string, string, int) (wire.CommandProgramReceiptPage, error) {
	exit := 0
	return wire.CommandProgramReceiptPage{Activities: []wire.CommandActivity{p.activity},
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

func generated(request briefing.Request) (calls int, lookups int) {
	for _, section := range request.Sections {
		switch section.Mode {
		case briefing.Generated:
			calls++
		case briefing.Lookup:
			lookups++
		}
	}
	return calls, lookups
}

// Each policy row asks the model for at most the sections the policy names.
func TestGenerationPolicyMatrix(t *testing.T) {
	read := briefing.CausalRead{Found: true, Call: briefing.CallExcerpt{Name: "command", Args: `{"command":"go test ./..."}`}}
	cases := []struct {
		name     string
		activity wire.CommandActivity
		calls    int
		lookups  int
	}{
		{"execute settled", activity("execute", "completed", "completed"), 2, 0},
		{"execute running", activity("execute", "running", "running"), 1, 0},
		{"failed inspect", activity("inspect", "failed", "failed"), 2, 0},
		{"interrupted mutate", activity("mutate", "interrupted", "interrupted"), 2, 0},
		{"steps differ", activity("inspect", "completed", "completed", "failed"), 2, 0},
		{"inspect completed", activity("inspect", "completed", "completed", "completed"), 0, 1},
		{"mutate running", activity("mutate", "running", "running"), 0, 1},
		{"refused", activity("execute", "refused", "refused"), 0, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			profile := New(sessions{projectID: project}, &programs{activity: c.activity}, cause{read: read})
			request, err := profile.Resolve(t.Context(), commandTarget())
			testutil.FailErr(t, "resolve", err)
			if calls, lookups := generated(request); calls != c.calls || lookups != c.lookups {
				t.Fatalf("calls %d lookups %d; want %d %d", calls, lookups, c.calls, c.lookups)
			}
			if last := request.Sections[len(request.Sections)-1]; last.ID != "outcome" || last.Mode != briefing.Deterministic {
				t.Fatalf("outcome missing: %+v", last)
			}
		})
	}
	tool := New(sessions{projectID: project}, &programs{}, cause{read: briefing.CausalRead{Found: true, Call: briefing.CallExcerpt{Name: "read",
		Result: &briefing.ResultExcerpt{Tool: "read", Outcome: "success", Content: "214 lines", Ref: "message:r"}}}})
	raw, _ := json.Marshal(wire.ToolActivityBriefingTarget{SessionID: "s", ToolCallIds: []string{"call"}})
	request, err := tool.Resolve(t.Context(), briefing.Target{ProjectID: project, Ref: raw})
	testutil.FailErr(t, "resolve tool", err)
	if calls, _ := generated(request); calls != 2 {
		t.Fatalf("settled tool activity asked for %d sections", calls)
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
