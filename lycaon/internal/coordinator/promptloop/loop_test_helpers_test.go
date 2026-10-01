package promptloop

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func taskCallArgs(agentType, goal string) map[string]any {
	return map[string]any{
		"agent_type": agentType,
		"brief":      map[string]any{"goal": goal, "done_when": []any{"Return grounded results."}},
	}
}

func taskArgsWithScope(goal, path string) map[string]any {
	args := taskCallArgs("implementer", goal)
	args["scope"] = map[string]any{"paths": []any{path}}
	return args
}

// emptyEvidenceLedger satisfies closeout grounding in unit tests when no ledger is wired.
type emptyEvidenceLedger struct{}

func (emptyEvidenceLedger) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, nil
}

func (emptyEvidenceLedger) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, nil
}

// NewPromptLoopForTest wires a PromptLoop for unit and integration tests.
func NewPromptLoopForTest(deps PromptLoopDeps) *PromptLoop {
	if deps.Policy == nil && deps.Tools != nil {
		deps.Policy = registryTestPolicy{metas: deps.Tools.List()}
	}
	if deps.UpdateMessage == nil {
		deps.UpdateMessage = func(_ context.Context, _, _ string, _ api.Message) error { return nil }
	}
	if deps.EvidenceLedger == nil {
		deps.EvidenceLedger = emptyEvidenceLedger{}
	}
	if deps.EvaluateCloseoutBlock == nil {
		// Unit harnesses that assert OAR closeout blocks wire a real EvaluateCloseoutBlock.
		deps.EvaluateCloseoutBlock = func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) {
			return nil, nil
		}
	}
	return &PromptLoop{Deps: deps}
}

type registryTestPolicy struct {
	metas []tools.ToolMeta
}

func (p registryTestPolicy) ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta {
	return append([]tools.ToolMeta(nil), p.metas...)
}

func (registryTestPolicy) EvaluateInvoke(context.Context, *api.Session, string, map[string]any) error {
	return nil
}
