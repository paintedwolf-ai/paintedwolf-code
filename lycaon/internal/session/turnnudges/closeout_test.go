package turnnudges

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	setupanchor "github.com/lycaon/lycaon/internal/testsetup/anchor"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordedKick struct {
	prompts.PromptTemplateEngine
	data map[string]any
}

func (r *recordedKick) RenderKick(_ context.Context, name string, data map[string]any) (string, error) {
	r.data = data
	return name, nil
}

func TestCloseoutPreservesCancellationAndIterationContext(t *testing.T) {
	setupanchor.Install()
	r := &recordedKick{}
	s := New(turnload.NewLedger(), nil, nil)
	s.SetPrompts(r)
	child := &api.Session{ID: "child", ParentSessionID: "parent"}
	n := s.Closeout(t.Context(), child, "implementer", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutCanceled, CancelReason: "human stopped the job"})
	if n.SignalID != string(anchor.WorkerCancelCloseout) || n.Content == "" || r.data["cancel_reason"] != "human stopped the job" {
		t.Fatalf("cancellation guidance=%+v data=%v", n, r.data)
	}
	n = s.Closeout(t.Context(), &api.Session{ID: "parent"}, "coordinator", promptloop.TurnCloseoutCause{Reason: promptloop.TurnCloseoutLLMTimeout})
	if n.SignalID != string(anchor.TurnCloseout) || n.Content == "" || r.data["llm_timeout"] != true {
		t.Fatalf("timeout guidance=%+v data=%v", n, r.data)
	}
	n = s.IterationRunway(t.Context(), &api.Session{ID: "parent"}, "coordinator", 3)
	if n.SignalID != string(anchor.TurnIterationsLow) || r.data["remaining"] != 3 {
		t.Fatalf("iteration guidance=%+v data=%v", n, r.data)
	}
	n = s.BudgetAnswer(anchor.WorkerBudgetRaised)(t.Context(), child, 4, 9)
	if n.Content == "" || r.data["tool_loops_used"] != 4 || r.data["max_tool_loops"] != 9 || r.data["remaining"] != 5 {
		t.Fatalf("budget guidance=%+v data=%v", n, r.data)
	}
	if n = s.BudgetAnswer(anchor.WorkerBudgetRaised)(t.Context(), &api.Session{ID: "parent"}, 4, 9); n.Content != "" {
		t.Fatalf("worker budget leaked to coordinator: %+v", n)
	}
}
