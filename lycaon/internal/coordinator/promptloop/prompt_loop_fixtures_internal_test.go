package promptloop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func staticCompactionConfig(context.Context, *api.Session) compaction.CompactionConfig {
	return compaction.DefaultCompactionConfig()
}

// The fixture guard uses a shorter repetition limit.
const doomLoopMaxAttemptsTest = 4

type recordingToolPolicy struct {
	listCalls []string
}

type frameCheckingToolPolicy struct {
	eval rules.EvalContext
}

func (p *frameCheckingToolPolicy) ListForPrompt(ctx context.Context, sess *api.Session, _ string) []tools.ToolMeta {
	p.eval, _ = toolpolicy.BuildEvalContext(ctx, toolpolicy.EngineDeps{}, sess, "read", nil)
	return promptLoopFixtureTools()
}

func (*frameCheckingToolPolicy) EvaluateInvoke(context.Context, *api.Session, string, map[string]any) error {
	return nil
}

type countingCoordinatorFrameSource struct {
	calls int
}

func (s *countingCoordinatorFrameSource) BuildCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error) {
	s.calls++
	return inject.CoordinatorTurnFrame{WorkflowRevision: int64(s.calls)}, nil
}

type staticCoordinatorFrameSource struct {
	frame inject.CoordinatorTurnFrame
}

func (s staticCoordinatorFrameSource) BuildCoordinatorTurnFrame(context.Context, string, *api.Session) (inject.CoordinatorTurnFrame, error) {
	return s.frame, nil
}

func (p *recordingToolPolicy) ListForPrompt(ctx context.Context, sess *api.Session, profileID string) []tools.ToolMeta {
	p.listCalls = append(p.listCalls, profileID)
	return promptLoopFixtureTools()
}

func (p *recordingToolPolicy) EvaluateInvoke(ctx context.Context, sess *api.Session, toolName string, args map[string]any) error {
	return nil
}

func promptLoopFixtureTools() []tools.ToolMeta {
	names := []string{"read", "write", "state_update", "task", "summarize"}
	metas := make([]tools.ToolMeta, len(names))
	for i, name := range names {
		metas[i] = tools.ToolMeta{Name: name, ArgsSchema: map[string]any{"type": "object"}}
	}
	return metas
}

type usageStreamLLM struct {
	usage modelcall.TokenUsage
}

type immediateFailingStreamLLM struct {
	err error
}

func (s *immediateFailingStreamLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, s.err
}

func (s *immediateFailingStreamLLM) Stream(context.Context, modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, s.err
}

type accountingFailureTracker struct {
	cost.CostTracker
	err error
}

func (t accountingFailureTracker) BeginCall(context.Context, cost.UsageEvent) error {
	return t.err
}

func (t accountingFailureTracker) MarkCallUnknown(context.Context, string) error {
	return t.err
}

func (t accountingFailureTracker) VoidCall(context.Context, string) error {
	return t.err
}

func (t accountingFailureTracker) RecordUsage(context.Context, cost.UsageEvent) error {
	return t.err
}

func (s *usageStreamLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return &modelcall.Completion{Content: "ok", Usage: s.usage}, nil
}

func (s *usageStreamLLM) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk, 1)
	go func() {
		defer close(ch)
		ch <- modelcall.StreamChunk{Content: "ok", Usage: s.usage, Done: true}
	}()
	return ch, nil
}

// gatedUsageStreamLLM exposes stream event ordering.
type gatedUsageStreamLLM struct {
	usage   modelcall.TokenUsage
	started chan struct{}
	release chan struct{}
}

func newGatedUsageStreamLLM(usage modelcall.TokenUsage) *gatedUsageStreamLLM {
	return &gatedUsageStreamLLM{
		usage:   usage,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *gatedUsageStreamLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return &modelcall.Completion{Content: "ok", Usage: s.usage}, nil
}

func (s *gatedUsageStreamLLM) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	close(s.started)
	ch := make(chan modelcall.StreamChunk, 1)
	go func() {
		defer close(ch)
		<-s.release
		ch <- modelcall.StreamChunk{Content: "ok", Usage: s.usage, Done: true}
	}()
	return ch, nil
}

// gatedFailingStreamLLM exposes failure event ordering.
type gatedFailingStreamLLM struct {
	err     error
	started chan struct{}
	release chan struct{}
}

func newGatedFailingStreamLLM(err error) *gatedFailingStreamLLM {
	return &gatedFailingStreamLLM{
		err:     err,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (s *gatedFailingStreamLLM) Complete(context.Context, modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, s.err
}

func (s *gatedFailingStreamLLM) Stream(_ context.Context, _ modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	close(s.started)
	ch := make(chan modelcall.StreamChunk, 1)
	go func() {
		defer close(ch)
		<-s.release
		ch <- modelcall.StreamChunk{Err: s.err, Done: true}
	}()
	return ch, nil
}

// Failed calls publish a terminal event.
func drainTopic(t *testing.T, ch <-chan api.EventEnvelope, wait time.Duration) []api.EventEnvelope {
	t.Helper()
	deadline := time.After(wait)
	var out []api.EventEnvelope
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				return out
			}
			if env.Topic == api.EventTopicLLM {
				out = append(out, env)
			}
		case <-deadline:
			return out
		}
	}
}

type fakeDoomLoop struct {
	surveyOutcomeRecorder
	allowed bool
	count   int
}

func (f *fakeDoomLoop) Check(context.Context, string, string, string, map[string]any) (bool, int, string, error) {
	return f.allowed, f.count, "", nil
}

func (f *fakeDoomLoop) RecordAttempt(context.Context, string, string, string, map[string]any, string, bool) error {
	return nil
}

func loadCoordinatorTestHintConfig(t *testing.T) *guidance.HintConfig {
	t.Helper()
	cfg, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "guidance.LoadHintConfigStock failed", err)
	return cfg
}

type memoryDoomLoopGuard struct {
	surveyOutcomeRecorder
	counts map[string]map[string]int // sessionID -> key -> count
}

func (g *memoryDoomLoopGuard) Check(_ context.Context, sessionID, _, tool string, args map[string]any) (bool, int, string, error) {
	key, _ := stableKey(tool, args)
	if g.counts[sessionID] == nil {
		return true, 0, "", nil
	}
	n := g.counts[sessionID][key]
	return n < doomLoopMaxAttemptsTest, n, "", nil
}

func (g *memoryDoomLoopGuard) RecordAttempt(_ context.Context, sessionID, _, tool string, args map[string]any, _ string, _ bool) error {
	key, _ := stableKey(tool, args)
	if g.counts[sessionID] == nil {
		g.counts[sessionID] = map[string]int{}
	}
	g.counts[sessionID][key]++
	return nil
}

type surveyOutcomeRecorder struct {
	searchOutcomes []bool
}

func (g *surveyOutcomeRecorder) RecordSearchOutcome(_ context.Context, _, _ string, _ map[string]any, foundMaterial bool) (int, error) {
	g.searchOutcomes = append(g.searchOutcomes, foundMaterial)
	return 0, nil
}

func stableKey(tool string, args map[string]any) (string, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return tool, err
	}
	return tool + ":" + string(b), nil
}

func kickTestRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

func discoveredPromptProfileIDs(t *testing.T) []string {
	t.Helper()
	seen := map[string]struct{}{prompts.CoordinatorProfileID: {}}
	packs := filepath.Join(kickTestRoot(t), "config", "packs")
	err := filepath.WalkDir(packs, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".yaml") {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, packs))
		switch {
		case strings.Contains(rel, "/tools/profiles/"):
			seen[strings.TrimSuffix(d.Name(), ".yaml")] = struct{}{}
		case strings.Contains(rel, "/agents/"):
			if id := firstYAMLID(path); id != "" {
				seen[id] = struct{}{}
			}
		}
		return nil
	})
	testutil.FailErr(t, "walk prompt profile ids", err)
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out
}

func firstYAMLID(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "id:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// refusalForTest builds structured test refusals.
func refusalForTest(block string) *guidance.Refusal {
	code := ""
	for _, line := range strings.Split(block, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Code:"); ok {
			code = strings.TrimSpace(rest)
			break
		}
	}
	return guidance.NewRefusal(code, block)
}

func (*fakeDoomLoop) ResolveRejection(context.Context, string, string, map[string]any, string) error {
	return nil
}
func (*memoryDoomLoopGuard) ResolveRejection(context.Context, string, string, map[string]any, string) error {
	return nil
}
