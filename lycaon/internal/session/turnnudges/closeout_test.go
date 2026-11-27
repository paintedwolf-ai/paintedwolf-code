package turnnudges

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	setupanchor "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordedKick struct {
	prompts.PromptTemplateEngine
	reader prompts.Reader
	data   map[string]any
}

func (r *recordedKick) RenderKick(_ context.Context, reader prompts.Reader, name string, data map[string]any) (string, error) {
	r.reader = reader
	r.data = data
	return name, nil
}

type failingSpendKick struct {
	prompts.PromptTemplateEngine
	called bool
}

func (r *failingSpendKick) RenderKick(context.Context, prompts.Reader, string, map[string]any) (string, error) {
	r.called = true
	return "partial", errors.New("render failed")
}

// A failed spend render grants no wind-down round and delivers no partial text.
func TestFailedSpendSoftStopDoesNotDeliverPartialGuidance(t *testing.T) {
	setupanchor.Install()
	r := &failingSpendKick{}
	s := New(turnload.NewLedger(), nil)
	s.SetPrompts(r)
	n := s.SpendSoftStop(t.Context(), &api.Session{ID: "parent"}, surfaceReader(t, "implement_investigate", api.SessionModeAgent))
	if !r.called || !n.Empty() {
		t.Fatalf("render called=%v nudge=%+v", r.called, n)
	}
}

type fixedSurface string

func (f fixedSurface) PromptTurnSurfaceID(string) string { return string(f) }

type fixedSpend bool

func (f fixedSpend) ClaimWarning(context.Context, *api.Session, float64) bool { return bool(f) }

// surfaceReader is the declared request of a coordinator surface with loaded
// tools promoted.
func surfaceReader(t *testing.T, surfaceID string, mode api.SessionMode, loaded ...string) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load profiles", err)
	plan, err := prompts.CompileSurfacePlan(surfaceID, 1)
	testutil.FailErr(t, "compile surface", err)
	plan = plan.Promote(loaded...)
	reader, err := prompts.NewReader(prompts.HostFacts{SessionMode: string(mode), Profiles: profiles, RootCount: 1, WebSearch: true},
		prompts.TurnProjection{ProfileID: prompts.CoordinatorProfileID, SurfaceID: surfaceID, Plan: plan, Offered: plan.ImmediateNames()})
	testutil.FailErr(t, "surface reader", err)
	return reader
}

// profileReader is a worker profile's declared request.
func profileReader(t *testing.T, profileID string) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load profiles", err)
	reader, err := prompts.NewReader(prompts.HostFacts{Profiles: profiles, RootCount: 1, WebSearch: true},
		prompts.TurnProjection{ProfileID: profileID, Declared: true})
	testutil.FailErr(t, "profile reader", err)
	return reader
}

func TestCloseoutPreservesCancellationAndIterationContext(t *testing.T) {
	setupanchor.Install()
	r := &recordedKick{}
	s := New(turnload.NewLedger(), nil)
	s.SetPrompts(r)
	child := &api.Session{ID: "child", ParentSessionID: "parent"}
	n := s.Closeout(t.Context(), child, profileReader(t, "implement"), promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutCanceled, CancelReason: "human stopped the job"})
	if n.SignalID != string(anchor.WorkerCancelCloseout) || n.Content == "" || !r.reader.Valid() || r.data["cancel_reason"] != "human stopped the job" {
		t.Fatalf("cancellation guidance=%+v readerValid=%v data=%v", n, r.reader.Valid(), r.data)
	}
	if got := r.reader.Offered(); !reflect.DeepEqual(got, []string{"complete_leg"}) || len(r.reader.Requestable()) != 0 {
		t.Fatalf("worker closeout reader offered=%v requestable=%v, want only complete_leg", got, r.reader.Requestable())
	}
	n = s.Closeout(t.Context(), &api.Session{ID: "parent"}, surfaceReader(t, "implement_investigate", api.SessionModeAgent), promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
	if n.SignalID != string(anchor.TurnCloseout) || n.Content == "" || !r.reader.Valid() || r.data["llm_timeout"] != true || r.data["session_command_mutation_capable"] != true {
		t.Fatalf("timeout guidance=%+v readerValid=%v data=%v", n, r.reader.Valid(), r.data)
	}
	if len(r.reader.Offered()) != 0 || r.reader.RootCount() != 1 {
		t.Fatalf("coordinator closeout reader offered=%v roots=%d, want no tools on the request's host facts", r.reader.Offered(), r.reader.RootCount())
	}
	n = s.IterationRunway(t.Context(), &api.Session{ID: "parent"}, surfaceReader(t, "implement_investigate", api.SessionModeAgent), 3)
	if n.SignalID != string(anchor.TurnIterationsLow) || !r.reader.Valid() || r.data["remaining"] != 3 {
		t.Fatalf("iteration guidance=%+v readerValid=%v data=%v", n, r.reader.Valid(), r.data)
	}
	n = s.IterationRunway(t.Context(), &api.Session{ID: "parent"}, prompts.Reader{}, 3)
	if n.SignalID != string(anchor.TurnIterationsLow) || !strings.HasPrefix(n.Content, "[host:") {
		t.Fatalf("a nudge without a request reader = %+v, want the host marker", n)
	}
}

// Forced final turns read exactly what Closeout sends for their surface.
func TestForcedFinalTurnKicksReadWhatCloseoutSends(t *testing.T) {
	setupanchor.Install()
	reference := regexp.MustCompile(`(?:\{\{\s*|\{%\s*if\s+)(\w+)`)
	for _, tc := range []struct {
		stem    string
		surface string
		worker  bool
		pack    string
	}{
		{stem: "coordinator-turn-closeout", surface: "implement_dispatch", pack: "guidance/coordinator-turn-closeout.md"},
		{stem: "coordinator-closeout", surface: "implement_synthesis", pack: "guidance/coordinator-closeout.md"},
	} {
		r := &recordedKick{}
		s := New(turnload.NewLedger(), nil)
		s.SetPrompts(r)
		s.SetSurface(fixedSurface(tc.surface))
		s.Closeout(t.Context(), &api.Session{ID: "parent", WorkspacePath: "/tmp/proj"}, surfaceReader(t, tc.surface, api.SessionModeAgent), promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
		raw, _, err := (prompts.BundledLayout{}).ReadBundled(tc.pack)
		testutil.FailErr(t, "read bundled template "+tc.stem, err)
		reads := map[string]bool{}
		for _, match := range reference.FindAllStringSubmatch(string(raw), -1) {
			reads[match[1]] = true
		}
		for name := range reads {
			if _, sent := r.data[name]; !sent {
				t.Errorf("%s reads %q, which Closeout does not send", tc.stem, name)
			}
		}
		for name := range r.data {
			if !reads[name] {
				t.Errorf("%s is sent %q and does not read it", tc.stem, name)
			}
		}
	}
}

// Closeout nudges carry the recipient's mutation capability: the value
// follows the surface, session mode and worker profile.
func TestTurnNudgesCarryRecipientMutationCapability(t *testing.T) {
	setupanchor.Install()
	ledger := turnload.NewLedger()
	agentSess := &api.Session{ID: "mut-coord", Mode: api.SessionModeAgent, WorkspacePath: "/tmp/proj"}
	askSess := &api.Session{ID: "ro-coord", Mode: api.SessionModeAsk, WorkspacePath: "/tmp/proj"}

	for _, tc := range []struct {
		surface string
		sess    *api.Session
		capable bool
	}{
		{"plan_execute", agentSess, true},
		{"orchestrate_plan", agentSess, false},
		{"orchestrate_plan", askSess, false},
	} {
		rec := &recordedKick{}
		svc := New(ledger, fixedSpend(true))
		svc.SetPrompts(rec)
		svc.SetSurface(fixedSurface(tc.surface))
		svc.Closeout(t.Context(), tc.sess, surfaceReader(t, tc.surface, tc.sess.Mode), promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
		if rec.data["session_command_mutation_capable"] != tc.capable {
			t.Errorf("%s %s closeout session_command_mutation_capable=%v, want %t", tc.surface, tc.sess.Mode, rec.data["session_command_mutation_capable"], tc.capable)
		}
	}

	for _, tc := range []struct {
		agent, profile string
		capable        bool
	}{
		{"implementer", "implement", true},
		{"path-explorer", "explore_readonly", false},
	} {
		rec := &recordedKick{}
		svc := New(turnload.NewLedger(), nil)
		svc.SetPrompts(rec)
		child := &api.Session{ID: "w-" + tc.agent, ParentSessionID: "mut-coord", AgentType: tc.agent, Mode: api.SessionModeAgent, WorkspacePath: "/tmp/proj"}
		svc.Closeout(t.Context(), child, profileReader(t, tc.profile), promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
		if rec.data["leg_command_mutation_capable"] != tc.capable {
			t.Errorf("%s worker closeout leg_command_mutation_capable=%v, want %t", tc.agent, rec.data["leg_command_mutation_capable"], tc.capable)
		}
	}
}
