package turnnudges

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
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

type fixedSurface string

func (f fixedSurface) PromptTurnSurfaceID(string) string { return string(f) }

type fixedSpend bool

func (f fixedSpend) ClaimWarning(context.Context, *api.Session, float64) bool { return bool(f) }

type readOnlyProfiles struct {
	profiles []sandbox.ToolProfile
}

func (p readOnlyProfiles) ForSession(context.Context, *api.Session) []sandbox.ToolProfile {
	return p.profiles
}
func (p readOnlyProfiles) PromptToolProfile(_ context.Context, sess *api.Session) (string, error) {
	if sess != nil && sess.AgentType != "" {
		return prompts.ToolProfileForAgent(sess.AgentType)
	}
	return prompts.CoordinatorProfileID, nil
}
func (p readOnlyProfiles) CommandExternalHidden() bool { return false }

func TestCloseoutPreservesCancellationAndIterationContext(t *testing.T) {
	setupanchor.Install()
	r := &recordedKick{}
	s := New(turnload.NewLedger(), nil)
	s.SetPrompts(r)
	child := &api.Session{ID: "child", ParentSessionID: "parent"}
	n := s.Closeout(t.Context(), child, "implementer", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutCanceled, CancelReason: "human stopped the job"})
	if n.SignalID != string(anchor.WorkerCancelCloseout) || n.Content == "" || !r.reader.Valid() || r.data["cancel_reason"] != "human stopped the job" {
		t.Fatalf("cancellation guidance=%+v readerValid=%v data=%v", n, r.reader.Valid(), r.data)
	}
	n = s.Closeout(t.Context(), &api.Session{ID: "parent"}, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
	if n.SignalID != string(anchor.TurnCloseout) || n.Content == "" || !r.reader.Valid() || r.data["llm_timeout"] != true || r.data["session_command_mutation_capable"] != true {
		t.Fatalf("timeout guidance=%+v readerValid=%v data=%v", n, r.reader.Valid(), r.data)
	}
	n = s.IterationRunway(t.Context(), &api.Session{ID: "parent"}, "coordinator", 3)
	if n.SignalID != string(anchor.TurnIterationsLow) || !r.reader.Valid() || r.data["remaining"] != 3 {
		t.Fatalf("iteration guidance=%+v readerValid=%v data=%v", n, r.reader.Valid(), r.data)
	}
}

// AC-COMPAT-08: forced final turns read exactly what Closeout sends for their surface.
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
		s.Closeout(t.Context(), &api.Session{ID: "parent", WorkspacePath: "/tmp/proj"}, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
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

func TestTurnNudgesStateSpecificGuidancePairedWitnesses(t *testing.T) {
	setupanchor.Install()
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: configlayout.FindModuleRoot()})

	ledger := turnload.NewLedger()
	ledger.Activate("mut-coord", []string{"verify"}, "run verification")
	mutSvc := New(ledger, fixedSpend(true))
	mutSvc.SetPrompts(engine)
	mutSvc.SetSurface(fixedSurface("implement_investigate"))
	mutSess := &api.Session{ID: "mut-coord", Mode: api.SessionModeAgent, WorkspacePath: "/tmp/proj"}

	iterMut := mutSvc.IterationRunway(t.Context(), mutSess, "coordinator", 2)
	if !strings.Contains(iterMut.Content, "Complete the edit or command already in progress and run the verification") ||
		!strings.Contains(iterMut.Content, "staged command redirect") {
		t.Fatalf("mutating IterationRunway witness lost write/redirect guidance:\n%s", iterMut.Content)
	}
	spendMut := mutSvc.SpendRunway(t.Context(), mutSess, 5.0)
	if !strings.Contains(spendMut.Content, "finish the edit or command in progress and run the verification") {
		t.Fatalf("mutating SpendRunway witness lost write/verify guidance:\n%s", spendMut.Content)
	}
	mutTurnCloseoutSvc := New(ledger, fixedSpend(true))
	mutTurnCloseoutSvc.SetPrompts(engine)
	mutTurnCloseoutSvc.SetSurface(fixedSurface("plan_execute"))
	closeoutMut := mutTurnCloseoutSvc.Closeout(t.Context(), mutSess, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
	if !strings.Contains(closeoutMut.Content, "staged command redirect") {
		t.Fatalf("mutating coordinator TurnCloseout witness lost staged command redirect clause:\n%s", closeoutMut.Content)
	}

	roSurfaceAgent := New(turnload.NewLedger(), fixedSpend(true))
	roSurfaceAgent.SetPrompts(engine)
	roSurfaceAgent.SetSurface(fixedSurface("orchestrate_plan"))
	closeoutROAgent := roSurfaceAgent.Closeout(t.Context(), mutSess, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
	if strings.Contains(closeoutROAgent.Content, "staged command redirect") {
		t.Fatalf("read-only surface (orchestrate_plan) in Agent mode leaked staged command redirect clause:\n%s", closeoutROAgent.Content)
	}

	roSvc := New(turnload.NewLedger(), fixedSpend(true))
	roSvc.SetPrompts(engine)
	roSvc.SetSurface(fixedSurface("observe_investigate"))
	roSess := &api.Session{ID: "ro-coord", Mode: api.SessionModeAsk, WorkspacePath: "/tmp/proj"}

	iterRO := roSvc.IterationRunway(t.Context(), roSess, "coordinator", 2)
	if strings.Contains(iterRO.Content, "staged command redirect") || strings.Contains(iterRO.Content, "Complete the edit") {
		t.Fatalf("read-only IterationRunway witness leaked mutating edit/redirect instructions:\n%s", iterRO.Content)
	}
	if !strings.Contains(iterRO.Content, "Wrap up your current read or inspection") {
		t.Fatalf("read-only IterationRunway witness missing read-only wrap-up instruction:\n%s", iterRO.Content)
	}
	spendRO := roSvc.SpendRunway(t.Context(), roSess, 5.0)
	if strings.Contains(spendRO.Content, "finish the edit") {
		t.Fatalf("read-only SpendRunway witness leaked mutating edit instructions:\n%s", spendRO.Content)
	}
	if !strings.Contains(spendRO.Content, "Wrap up the read or inspection already in flight") {
		t.Fatalf("read-only SpendRunway witness missing read-only wrap-up instruction:\n%s", spendRO.Content)
	}
	roTurnCloseoutSvc := New(turnload.NewLedger(), fixedSpend(true))
	roTurnCloseoutSvc.SetPrompts(engine)
	roTurnCloseoutSvc.SetSurface(fixedSurface("orchestrate_plan"))
	closeoutRO := roTurnCloseoutSvc.Closeout(t.Context(), roSess, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
	if strings.Contains(closeoutRO.Content, "staged command redirect") {
		t.Fatalf("read-only coordinator TurnCloseout witness leaked staged command redirect clause:\n%s", closeoutRO.Content)
	}
	if !strings.Contains(closeoutRO.Content, "Summarize what earlier turns already established") {
		t.Fatalf("read-only coordinator TurnCloseout witness missing read-only timeout wrap-up:\n%s", closeoutRO.Content)
	}

	// Worker closeout paired witnesses: implementer (mutating) vs explore_readonly (read-only).
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load profiles", err)
	workerSvc := New(turnload.NewLedger(), nil)
	workerSvc.SetPrompts(engine)
	workerSvc.SetProfiles(readOnlyProfiles{profiles: profiles})

	implChild := &api.Session{ID: "w-impl", ParentSessionID: "mut-coord", AgentType: "implementer", Mode: api.SessionModeAgent, WorkspacePath: "/tmp/proj"}
	wMut := workerSvc.Closeout(t.Context(), implChild, "implement", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
	if !strings.Contains(wMut.Content, "staged command redirect") {
		t.Fatalf("mutating worker closeout witness missing staged command redirect clause:\n%s", wMut.Content)
	}

	scoutChild := &api.Session{ID: "w-scout", ParentSessionID: "mut-coord", AgentType: "path-explorer", Mode: api.SessionModeAgent, WorkspacePath: "/tmp/proj"}
	wRO := workerSvc.Closeout(t.Context(), scoutChild, "explore_readonly", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
	if strings.Contains(wRO.Content, "staged command redirect") {
		t.Fatalf("read-only worker closeout witness leaked staged command redirect clause:\n%s", wRO.Content)
	}
	if !strings.Contains(wRO.Content, "what earlier observations already established") {
		t.Fatalf("read-only worker closeout witness missing read-only follow-up guidance:\n%s", wRO.Content)
	}
}
