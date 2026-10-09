package toolpolicy

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingRuleFunc func(context.Context, rules.EvalContext) (*rules.RuleOutcome, error)

func (f recordingRuleFunc) Evaluate(ctx context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error) {
	return f(ctx, eval)
}

func TestListingCapturesFactsOnceAndInvocationReadsAgain(t *testing.T) {
	captures := 0
	phase := "initial"
	var events []string
	var evaluated []rules.EvalContext
	eng := NewEngine(EngineDeps{
		ToolLister: listInvoker{metas: []tools.ToolMeta{{Name: "read"}, {Name: "write"}}},
		Workflows: func(context.Context, string) (WorkflowSnapshot, error) {
			captures++
			events = append(events, "capture")
			return WorkflowSnapshot{Phase: phase}, nil
		},
		PreInvoke: func(_ context.Context, _ *api.Session, tool string, _ map[string]any) error {
			events = append(events, "guard:"+tool)
			return nil
		},
		Rules: recordingRuleFunc(func(_ context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error) {
			events = append(events, "rules:"+eval.ToolName)
			evaluated = append(evaluated, eval)
			phase = "changed"
			return nil, nil
		}),
	})
	sess := &api.Session{ID: "session"}
	if listed := eng.ListForPrompt(t.Context(), sess, "coordinator"); len(listed) != 2 {
		t.Fatalf("listed tools=%v", listed)
	}
	want := []string{"guard:read", "capture", "rules:read", "guard:write", "rules:write"}
	if !reflect.DeepEqual(events, want) || captures != 1 {
		t.Fatalf("events=%v captures=%d", events, captures)
	}
	for _, eval := range evaluated {
		if eval.Phase != "initial" || eval.ToolArgs != nil {
			t.Fatalf("inconsistent listing facts: %+v", eval)
		}
	}
	args := map[string]any{"path": "current"}
	if err := eng.EvaluateInvoke(t.Context(), sess, "write", args); err != nil {
		t.Fatalf("invoke after listing: %v", err)
	}
	last := evaluated[len(evaluated)-1]
	if captures != 2 || last.Phase != "changed" || last.ToolName != "write" || last.ToolArgs["path"] != "current" {
		t.Fatalf("stale invocation facts: captures=%d eval=%+v", captures, last)
	}
	eng.ListForPrompt(t.Context(), sess, "coordinator")
	if captures != 3 {
		t.Fatalf("second listing reused capture: %d", captures)
	}
}

func TestListingIsolatesMutableFactsBetweenTools(t *testing.T) {
	seen := 0
	eng := NewEngine(EngineDeps{
		ToolLister: listInvoker{metas: []tools.ToolMeta{{Name: "read"}, {Name: "write"}}},
		Workflows: func(context.Context, string) (WorkflowSnapshot, error) {
			return WorkflowSnapshot{
				AllowedAgents: []string{"worker"}, ManifestRules: []string{"rule"}, Vars: map[string]any{"nested": map[string]any{"value": "original"}},
			}, nil
		},
		Rules: recordingRuleFunc(func(_ context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error) {
			seen++
			if eval.AllowedAgents[0] != "worker" || eval.ManifestRules[0] != "rule" || eval.Vars["nested"].(map[string]any)["value"] != "original" {
				t.Fatalf("shared mutable facts: %+v", eval)
			}
			eval.AllowedAgents[0] = "changed"
			eval.ManifestRules[0] = "changed"
			eval.Vars["nested"].(map[string]any)["value"] = "changed"
			return nil, nil
		}),
	})
	eng.ListForPrompt(t.Context(), &api.Session{ID: "session"}, "coordinator")
	if seen != 2 {
		t.Fatalf("evaluated=%d", seen)
	}
}

func TestListingDoesNotCaptureForBypassedOrRejectedTools(t *testing.T) {
	captures := 0
	eng := NewEngine(EngineDeps{
		ToolLister: listInvoker{metas: []tools.ToolMeta{{Name: "read"}, {Name: "write", Deferred: true}, {Name: "mcp_search", Source: tools.ToolSourceMCP}}},
		Workflows:  func(context.Context, string) (WorkflowSnapshot, error) { captures++; return WorkflowSnapshot{}, nil },
		PreInvoke:  func(context.Context, *api.Session, string, map[string]any) error { return errors.New("guard rejected") },
		Rules:      staticRuleEvaluator{},
	})
	if listed := eng.ListForPrompt(t.Context(), &api.Session{ID: "session"}, "coordinator"); len(listed) != 2 || captures != 0 {
		t.Fatalf("listed=%v captures=%d", listed, captures)
	}
}

func TestWorkflowCaptureFailureDoesNotBecomeAbsentState(t *testing.T) {
	for _, captureErr := range []error{errors.New("store unavailable"), settingsoverlay.ErrFormatInvalid} {
		eng := NewEngine(EngineDeps{
			ToolLister: listInvoker{metas: []tools.ToolMeta{{Name: "read"}, {Name: "write"}}},
			Workflows:  func(context.Context, string) (WorkflowSnapshot, error) { return WorkflowSnapshot{}, captureErr },
			Rules:      staticRuleEvaluator{},
		})
		sess := &api.Session{ID: "session"}
		if listed := eng.ListForPrompt(t.Context(), sess, "coordinator"); len(listed) != 0 {
			t.Fatalf("failed capture listed=%v", listed)
		}
		if err := eng.EvaluateInvoke(t.Context(), sess, "read", nil); !errors.Is(err, captureErr) {
			t.Fatalf("capture error lost: %v", err)
		}
		err := eng.EvaluateInvoke(t.Context(), sess, "write", map[string]any{"path": settingsoverlay.Rel(settingsoverlay.FormatFileName)})
		if !errors.Is(err, captureErr) {
			t.Fatalf("failed capture allowed repair: %v", err)
		}
	}
}

func TestListingDetachesSourceBeforeLaterPreInvoke(t *testing.T) {
	source := WorkflowSnapshot{AllowedAgents: []string{"worker"}, ManifestRules: []string{"rule"}, Vars: map[string]any{"nested": map[string]any{"value": "initial"}}}
	roots := []string{"initial-root"}
	seen := 0
	eng := NewEngine(EngineDeps{
		ToolLister:       listInvoker{metas: []tools.ToolMeta{{Name: "read"}, {Name: "write"}}},
		Workflows:        func(context.Context, string) (WorkflowSnapshot, error) { return source, nil },
		OverlayRootPaths: func(context.Context, *api.Session) []string { return roots },
		PreInvoke: func(_ context.Context, _ *api.Session, tool string, _ map[string]any) error {
			if tool == "write" {
				source.AllowedAgents[0] = "changed"
				source.ManifestRules[0] = "changed"
				source.Vars["nested"].(map[string]any)["value"] = "changed"
				roots[0] = "changed"
			}
			return nil
		},
		Rules: recordingRuleFunc(func(_ context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error) {
			seen++
			want := "worker"
			if seen > 2 {
				want = "changed"
			}
			if eval.AllowedAgents[0] != want {
				t.Fatalf("source changed captured roster: %+v", eval)
			}
			if seen <= 2 && (eval.ManifestRules[0] != "rule" || eval.OverlayRootPaths[0] != "initial-root" || eval.Vars["nested"].(map[string]any)["value"] != "initial") {
				t.Fatalf("source changed captured facts: %+v", eval)
			}
			return nil, nil
		}),
	})
	sess := &api.Session{ID: "session"}
	eng.ListForPrompt(t.Context(), sess, "coordinator")
	if err := eng.EvaluateInvoke(t.Context(), sess, "read", nil); err != nil {
		t.Fatalf("invoke current facts: %v", err)
	}
	if seen != 3 {
		t.Fatalf("evaluations=%d", seen)
	}
}

func TestListingAndInvocationHonorBoundFrame(t *testing.T) {
	frame := &inject.CoordinatorTurnFrame{RunContext: api.CoordinatorRunContext{CurrentPhase: "bound", AllowedAgents: []string{"fallback"}}, Roster: &inject.AgentRoster{Effective: []string{}}}
	seen := 0
	eng := NewEngine(EngineDeps{
		ToolLister: listInvoker{metas: []tools.ToolMeta{{Name: "read"}, {Name: "write"}}},
		Workflows: func(context.Context, string) (WorkflowSnapshot, error) {
			t.Fatal("bound frame triggered live capture")
			return WorkflowSnapshot{}, nil
		},
		Rules: recordingRuleFunc(func(_ context.Context, eval rules.EvalContext) (*rules.RuleOutcome, error) {
			seen++
			if eval.Phase != "bound" || eval.AllowedAgents == nil || len(eval.AllowedAgents) != 0 {
				t.Fatalf("bound authority changed: %+v", eval)
			}
			return nil, nil
		}),
	})
	ctx := WithCoordinatorTurnFrame(t.Context(), frame)
	sess := &api.Session{ID: "session"}
	eng.ListForPrompt(ctx, sess, "coordinator")
	if err := eng.EvaluateInvoke(ctx, sess, "read", map[string]any{"path": "x"}); err != nil {
		t.Fatalf("bound invocation: %v", err)
	}
	if seen != 3 {
		t.Fatalf("evaluations=%d", seen)
	}
}
