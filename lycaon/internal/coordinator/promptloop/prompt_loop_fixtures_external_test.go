package promptloop_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

func staticCompactionConfig(context.Context, *api.Session) compaction.CompactionConfig {
	return compaction.DefaultCompactionConfig()
}

type recordingToolPolicy struct {
	listCalls []string
}

func (p *recordingToolPolicy) ListForPrompt(ctx context.Context, sess *api.Session, profileID string) []tools.ToolMeta {
	p.listCalls = append(p.listCalls, profileID)
	return promptLoopFixtureTools()
}

func (p *recordingToolPolicy) EvaluateInvoke(ctx context.Context, sess *api.Session, toolName string, args map[string]any) error {
	return nil
}

type denyToolPolicy struct{}

func (denyToolPolicy) ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta {
	return []tools.ToolMeta{{Name: "read"}}
}

func promptLoopFixtureTools() []tools.ToolMeta {
	names := []string{"read", "write", "state_update", "task", "summarize"}
	metas := make([]tools.ToolMeta, len(names))
	for i, name := range names {
		metas[i] = tools.ToolMeta{Name: name, ArgsSchema: map[string]any{"type": "object"}}
	}
	return metas
}

func (denyToolPolicy) EvaluateInvoke(context.Context, *api.Session, string, map[string]any) error {
	return errors.New("RULE_DENY: blocked")
}

type fixedToolPolicy struct {
	metas []tools.ToolMeta
}

type staticCoordinatorContext struct {
	run api.CoordinatorRunContext
}

func (c staticCoordinatorContext) BuildCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error) {
	return inject.CoordinatorTurnFrame{RunContext: c.run}, nil
}

func investigateCoordinatorContext() staticCoordinatorContext {
	eligible := true
	return staticCoordinatorContext{run: api.CoordinatorRunContext{
		WorkflowID: "implement", CurrentPhase: "work",
		WorkflowInvestigateEligible: &eligible,
		PhaseCoordinatorSurface:     tools.SurfaceImplementInvestigate,
	}}
}

func (p *fixedToolPolicy) ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta {
	return p.metas
}

func (p *fixedToolPolicy) EvaluateInvoke(context.Context, *api.Session, string, map[string]any) error {
	return nil
}

func userHistory(content string) []api.Message {
	return []api.Message{{
		Role:    api.MessageRoleUser,
		Content: content,
	}}
}

// sequentialLLMClient returns fixed completions in order for multi-turn prompt loop tests.
type sequentialLLMClient struct {
	completions     []*modelcall.Completion
	idx             int
	toolsPerRequest []int
	requests        []modelcall.CompletionRequest
}

func (s *sequentialLLMClient) Complete(ctx context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.idx >= len(s.completions) {
		return &modelcall.Completion{Content: "unexpected extra turn"}, nil
	}
	out := *s.completions[s.idx]
	s.idx++
	return &out, nil
}

func (s *sequentialLLMClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	s.toolsPerRequest = append(s.toolsPerRequest, len(req.Tools))
	s.requests = append(s.requests, req)
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := s.Complete(ctx, req)
		if err != nil {
			return
		}
		if len(completion.ToolCalls) > 0 {
			if strings.TrimSpace(completion.Content) != "" {
				ch <- modelcall.StreamChunk{Content: completion.Content}
			}
			ch <- modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Done: true}
			return
		}
		ch <- modelcall.StreamChunk{Content: completion.Content, Done: true}
	}()
	return ch, nil
}

type tokenStreamingLLM struct {
	tokens []string
}

func (s tokenStreamingLLM) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return &modelcall.Completion{Content: strings.Join(s.tokens, "")}, nil
}

func (s tokenStreamingLLM) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		for i, tok := range s.tokens {
			ch <- modelcall.StreamChunk{Content: tok, Done: i == len(s.tokens)-1}
		}
	}()
	return ch, nil
}

type toolStreamingLLM struct {
	calls []api.ToolCall
	used  bool
}

func (s *toolStreamingLLM) Complete(_ context.Context, _ modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if s.used {
		return &modelcall.Completion{Content: "done"}, nil
	}
	s.used = true
	return &modelcall.Completion{ToolCalls: s.calls}, nil
}

func (s *toolStreamingLLM) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		if s.used {
			ch <- modelcall.StreamChunk{Content: "done", Done: true}
			return
		}
		s.used = true
		ch <- modelcall.StreamChunk{ToolCalls: s.calls, Progress: true}
		ch <- modelcall.StreamChunk{ToolCalls: s.calls, Done: true}
	}()
	return ch, nil
}

type richDenyRules struct {
	out *rules.RuleOutcome
}

type listedEvaluatingPolicy struct {
	metas     []tools.ToolMeta
	evaluator toolpolicy.Engine
}

func (p listedEvaluatingPolicy) ListForPrompt(context.Context, *api.Session, string) []tools.ToolMeta {
	return append([]tools.ToolMeta(nil), p.metas...)
}

func (p listedEvaluatingPolicy) EvaluateInvoke(ctx context.Context, sess *api.Session, toolName string, args map[string]any) error {
	return p.evaluator.EvaluateInvoke(ctx, sess, toolName, args)
}

func (r richDenyRules) Evaluate(context.Context, rules.EvalContext) (*rules.RuleOutcome, error) {
	return r.out, nil
}

type usageReportingLLM struct{ usage modelcall.TokenUsage }

func (u usageReportingLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return &modelcall.Completion{Content: "done", Usage: u.usage}, nil
}

func (u usageReportingLLM) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 1)
	go func() {
		defer close(ch)
		ch <- modelcall.StreamChunk{Content: "done", Usage: u.usage, Done: true}
	}()
	return ch, nil
}

type publishedProgress struct {
	snap       workerprogress.Snapshot
	checkpoint bool
}

type observedMessageStreams struct {
	promptloop.MessageStreams
	project func(context.Context, string, api.Message) error
}

func (s *observedMessageStreams) Project(ctx context.Context, sessionID string, message api.Message) error {
	return s.project(ctx, sessionID, message)
}

func phaseTestBlockPlane(t *testing.T) *tools.BlockPlane {
	t.Helper()
	root := testutil.CheckoutRoot(t)
	testutil.FailErr(t, "install anchors", anchorcatalog.InstallFile(filepath.Join(root, "lycaon/config/packs/painted-wolf/platform/host/anchors/catalog.yaml")))
	loader, err := oar.NewLoader(filepath.Join(root, "schemas"))
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadEffectivePolicy()
	testutil.FailErr(t, "load policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, oar.NewCounterStore())
	pipeline.EnableAnchor(oar.AnchorToolRejected)
	return &tools.BlockPlane{Pipeline: pipeline, Renderer: oar.NewRenderer(nil, nil)}
}
