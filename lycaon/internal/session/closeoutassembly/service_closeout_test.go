package closeoutassembly

import (
	"context"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

type closeoutSession struct{ Sessions }

func (closeoutSession) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ID: "parent", WorkspaceRootID: "primary"}, nil
}
func (closeoutSession) GetMessages(context.Context, string) ([]api.Message, error) { return nil, nil }

type closeoutRoots struct{ root string }

func (r closeoutRoots) Roots(context.Context, *api.Session) ([]projectroot.RootRef, error) {
	return []projectroot.RootRef{{ID: "primary", Path: r.root, IsPrimary: true}}, nil
}

type retainedLegs struct{ legs []api.Leg }

func (retainedLegs) DelegationBySessionID(string) (string, bool)           { return "delegation", true }
func (r retainedLegs) ListLegs(context.Context, string) ([]api.Leg, error) { return r.legs, nil }

func TestAssemblyRecoversCompletedLegsWithoutInventingCitations(t *testing.T) {
	s := New(closeoutSession{}, closeoutRoots{t.TempDir()}, nil, nil)
	s.SetDelegations(retainedLegs{[]api.Leg{
		{ID: "leg", WorkerID: "job", Title: "Review", Status: api.LegStatusComplete, Result: &api.WorkerResult{Summary: "Checked access controls."}},
		{ID: "duplicate", WorkerID: "job", Status: api.LegStatusComplete, Result: &api.WorkerResult{Summary: "Duplicate answer."}},
		{ID: "pending", Status: api.LegStatusPending, Result: &api.WorkerResult{Summary: "Unfinished assertion."}},
		{ID: "empty", Status: api.LegStatusComplete, Result: &api.WorkerResult{}},
	}})
	report, grounding := s.Assemble(t.Context(), "parent", "implement_synthesis", nil, "", 0)
	if !strings.Contains(report.Synthesis, "Review: Checked access controls.") || strings.Contains(report.Synthesis, "Duplicate") || strings.Contains(report.Synthesis, "Unfinished") {
		t.Fatalf("assembled report=%+v", report)
	}
	if len(report.CitedEvidence) != 0 || len(report.CitedURLs) != 0 {
		t.Fatalf("fabricated citations=%+v", report)
	}
	if grounding != nil && grounding.Traced {
		t.Fatalf("unobserved leg was traced: %+v", grounding)
	}
	report, _ = s.Assemble(t.Context(), "parent", "implement_synthesis", nil, "Human-facing draft.", 0)
	if report.Synthesis != "Human-facing draft." {
		t.Fatalf("draft replaced by retained legs: %+v", report)
	}
}
