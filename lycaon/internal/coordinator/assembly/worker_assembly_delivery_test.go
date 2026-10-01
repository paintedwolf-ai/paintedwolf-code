package assembly

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/pkg/api"
)

type deliverySpy struct {
	called bool
	got    []inject.SiblingNote
	notes  []inject.SiblingNote
}

func (s *deliverySpy) PrepareSiblingNotes(context.Context, string, int) (inject.SiblingNotePage, error) {
	return inject.SiblingNotePage{Notes: s.notes, Cursor: int64(len(s.notes))}, nil
}
func (s *deliverySpy) CommitSiblingNotes(_ context.Context, _, _ string, _ int64, notes []inject.SiblingNote) error {
	s.called = true
	s.got = notes
	return nil
}

func TestWorkerLegInjectRecordsDeliveredSiblingNotes(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	inj := prompts.NewInjectRenderer(pe)

	notes := []inject.SiblingNote{{ID: 11, HasBody: true, Agent: "job-2", Summary: "peer note", Ref: "AGENTS.md:1"}}
	spy := &deliverySpy{notes: notes}
	boardEng := NewBoardEngine(placementStubBoardBuilder{hash: "worker-board"}, nil, nil)
	boardEng.SetInjectRenderer(inj)

	deps := AssemblyDeps{
		Prompts:             pe,
		Injects:             inj,
		Limits:              func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerContext:       placementWorkerCtx{leg: inject.WorkerLegContext{LegID: "leg-1", Checklist: []string{"step"}}},
		SiblingNoteDelivery: spy,
		Board:               boardEng,
	}
	sess := &api.Session{
		ID: "delivery-worker", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
		Posture: api.SessionPostureBuild, AgentType: "implementer", WorkspacePath: t.TempDir(),
	}

	eng := &AssemblyEngine{}
	eng.SetDeps(deps)
	eng.BeginPromptTurn(sess.ID, "")
	if _, err := eng.BuildCompletionMessages(context.Background(), sess, []api.Message{
		{Role: api.MessageRoleUser, Content: "continue"},
	}, nil); err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}

	for _, reset := range []bool{false, true} {
		if reset {
			eng.BeginPromptTurn(sess.ID, "")
		}
		messages, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{{Role: api.MessageRoleUser, Content: "continue"}}, nil)
		if err != nil {
			t.Fatalf("rebuild request: %v", err)
		}
		found := false
		for _, message := range messages {
			for _, part := range message.ContentParts {
				if part.Source == "sibling_notes" {
					found = strings.Contains(part.Content, "finding 11") && strings.Contains(part.Content, "peer note")
					if part.Authority != api.ContentAuthorityNone || part.TrustTier != api.ContentTrustTierUntrusted {
						t.Fatal("peer content promoted to authority")
					}
				}
			}
		}
		if !found {
			t.Fatal("request lost retained finding identity or content")
		}
	}

	if spy.called {
		t.Fatal("assembly must not acknowledge delivery before a response")
	}
	if err := eng.CommitWorkerContext(context.Background(), sess.ID, "response-1"); err != nil {
		t.Fatalf("commit worker context: %v", err)
	}
	if !spy.called {
		t.Fatal("SiblingNoteDelivery.CommitSiblingNotes was not called for the worker leg")
	}
	if len(spy.got) != 1 || spy.got[0].Agent != "job-2" || spy.got[0].Ref != "AGENTS.md:1" {
		t.Fatalf("delivered notes = %+v want the single peer note", spy.got)
	}
}

func TestWorkerLegInjectSkipsDeliveryWhenNoNotes(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	inj := prompts.NewInjectRenderer(pe)

	spy := &deliverySpy{}
	boardEng := NewBoardEngine(placementStubBoardBuilder{hash: "worker-board"}, nil, nil)
	boardEng.SetInjectRenderer(inj)

	deps := AssemblyDeps{
		Prompts:             pe,
		Injects:             inj,
		Limits:              func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		WorkerContext:       placementWorkerCtx{leg: inject.WorkerLegContext{LegID: "leg-1", Checklist: []string{"step"}}},
		SiblingNoteDelivery: spy,
		Board:               boardEng,
	}
	sess := &api.Session{
		ID: "delivery-worker-none", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
		Posture: api.SessionPostureBuild, AgentType: orchestration.ProfileImplementer, WorkspacePath: t.TempDir(),
	}

	eng := &AssemblyEngine{}
	eng.SetDeps(deps)
	eng.BeginPromptTurn(sess.ID, "")
	if _, err := eng.BuildCompletionMessages(context.Background(), sess, []api.Message{
		{Role: api.MessageRoleUser, Content: "continue"},
	}, nil); err != nil {
		t.Fatalf("BuildCompletionMessages: %v", err)
	}
	if spy.called {
		t.Fatal("CommitSiblingNotes must not fire when no sibling notes were delivered")
	}
}

func TestWorkerGuidanceAppearsOnceOnStartAndResume(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..", "..")
	pe := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root})
	policy := api.Message{Role: api.MessageRoleSystem, Content: "Project policy sentinel: use the managed task runner.", Origin: api.MessageOriginProject, Authority: api.ContentAuthorityDeveloper, TrustTier: api.ContentTrustTierTrusted}
	eng := &AssemblyEngine{}
	eng.SetDeps(AssemblyDeps{Prompts: pe, Injects: prompts.NewInjectRenderer(pe), Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
		AgentsMDChain: func(context.Context, *api.Session, string) (api.Message, error) { return policy, nil },
		WorkerContext: placementWorkerCtx{leg: inject.WorkerLegContext{LegID: "leg", AgentsMDMessage: policy}},
	})
	sess := &api.Session{ID: "policy-worker", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, Posture: api.SessionPostureBuild, AgentType: "implementer", WorkspacePath: t.TempDir()}
	for _, resume := range []bool{false, false, true} {
		if resume {
			eng.BeginPromptTurn(sess.ID, "")
		}
		msgs, err := eng.BuildCompletionMessages(t.Context(), sess, []api.Message{{Role: api.MessageRoleUser, Content: "continue"}}, nil)
		if err != nil {
			t.Fatalf("build worker prompt: %v", err)
		}
		count := 0
		for _, msg := range msgs {
			count += strings.Count(msg.Content, policy.Content)
		}
		if count != 1 {
			t.Fatalf("resume=%v policy copies=%d", resume, count)
		}
	}
}
