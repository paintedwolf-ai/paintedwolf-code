package promptloop

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type testMessageStreams struct {
	cachelive   func(string, string, string, []string, int)
	cachereplay func(string, string, []string)
	project     func(context.Context, string, api.Message) error
	finish      func(context.Context, string)
}

func (s *testMessageStreams) CacheLive(sessionID, messageID, content string, tokens []string, generating int) {
	if s.cachelive != nil {
		s.cachelive(sessionID, messageID, content, tokens, generating)
	}
}
func (s *testMessageStreams) CacheReplay(messageID, content string, tokens []string) {
	if s.cachereplay != nil {
		s.cachereplay(messageID, content, tokens)
	}
}
func (s *testMessageStreams) Project(ctx context.Context, sessionID string, message api.Message) error {
	if s.project != nil {
		return s.project(ctx, sessionID, message)
	}
	return nil
}
func (s *testMessageStreams) Finish(ctx context.Context, sessionID string) {
	if s.finish != nil {
		s.finish(ctx, sessionID)
	}
}

// StoreDeps wires an in-memory store with an attached project and unchanged history.
func StoreDeps(st *store.Memory) PromptLoopDeps {
	return PromptLoopDeps{
		Context: ContextDeps{
			Limits: func(context.Context, *api.Session) settings.SessionLimits { return settings.DefaultSessionLimits() },
			BuildMessages: func(_ context.Context, _ *api.Session, history []api.Message, _ *inject.CoordinatorTurnFrame) ([]api.Message, error) {
				return history, nil
			},
			ProjectRootCount: func(context.Context, *api.Session) int { return 1 },
		},
		Projection: ProjectionDeps{
			AppendMessages: st.AppendMessages,
			UpdateMessage: func(ctx context.Context, sessionID, messageID string, msg api.Message) error {
				_, err := st.UpdateMessage(ctx, sessionID, messageID, msg)
				return err
			},
			Streams: &testMessageStreams{
				project: func(ctx context.Context, sessionID string, msg api.Message) error {
					return st.PatchLiveProjection(ctx, sessionID, msg.ID, msg.Content, msg.ToolCalls)
				},
			},
			AppendDraftVersion: st.AppendDraftVersion,
			CountDraftVersions: st.CountDraftVersions,
		},
	}
}

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
	if deps.Context.Policy == nil && deps.Context.Tools != nil {
		deps.Context.Policy = registryTestPolicy{metas: deps.Context.Tools.List()}
	}
	if deps.Projection.UpdateMessage == nil {
		deps.Projection.UpdateMessage = func(_ context.Context, _, _ string, _ api.Message) error { return nil }
	}
	if deps.Closeout.EvidenceLedger == nil {
		deps.Closeout.EvidenceLedger = emptyEvidenceLedger{}
	}
	if deps.Closeout.EvaluateCloseoutBlock == nil {
		// Unit harnesses that assert OAR closeout blocks wire a real EvaluateCloseoutBlock.
		deps.Closeout.EvaluateCloseoutBlock = func(context.Context, *api.Session, *oar.GuardContext) (*oar.Decision, error) {
			return nil, nil
		}
	}
	return NewPromptLoop(deps)
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
