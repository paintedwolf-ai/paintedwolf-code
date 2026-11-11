package llm

import (
	"context"
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"sync"
)

// MockConfig is the on-disk mock_llm.yaml shape.
type MockConfig struct {
	Responses    []MockResponseEntry `yaml:"responses"`
	VisionModels []string            `yaml:"vision_models,omitempty"`
}

// MockResponseEntry is one pattern-based mock completion rule.
type MockResponseEntry struct {
	Pattern      string         `yaml:"pattern"`
	Text         string         `yaml:"text,omitempty"`
	FollowUpText string         `yaml:"follow_up_text,omitempty"`
	AlwaysTool   bool           `yaml:"always_tool,omitempty"`
	ToolCalls    []MockToolCall `yaml:"tool_calls,omitempty"`
}

// MockToolCall is a tool call stub in mock config.
type MockToolCall struct {
	ID   string         `yaml:"id"`
	Name string         `yaml:"name"`
	Args map[string]any `yaml:"args"`
}

// LoadMockConfig reads the shipped mock LLM responses.
func LoadMockConfig() (*MockConfig, error) {
	data, err := config.Read(config.MockLLM)
	if err != nil {
		return nil, err
	}
	var cfg MockConfig
	if err := config.DecodeYAML(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// MockEnabled reports whether the runtime may use the mock LLM client.
// Mock is test-only: explicit LYCAON_LLM_MOCK=1 or an injected test client.
func MockEnabled(testClient modelcall.LLMClient) bool {
	if testClient != nil {
		return true
	}
	return MockOnlyFromEnv()
}

// ProviderUtilityCallsEnabled keeps background utility work off configured providers in test modes.
func ProviderUtilityCallsEnabled() bool {
	return !MockOnlyFromEnv() && !ManualOnlyFromEnv()
}

// RecordingClient wraps an LLMClient and captures completion requests for tests.
type RecordingClient struct {
	Inner modelcall.LLMClient
	mu    sync.Mutex
	last  modelcall.CompletionRequest
	all   []modelcall.CompletionRequest
}

// NewRecordingClient wraps inner for test observability.
func NewRecordingClient(inner modelcall.LLMClient) *RecordingClient {
	return &RecordingClient{Inner: inner}
}

// Complete records req and delegates to Inner.
func (c *RecordingClient) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	c.record(req)
	if c.Inner == nil {
		return &modelcall.Completion{}, nil
	}
	return c.Inner.Complete(ctx, req)
}

// Stream records req and delegates to Inner.
func (c *RecordingClient) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	c.record(req)
	if c.Inner == nil {
		ch := make(chan modelcall.StreamChunk)
		close(ch)
		return ch, nil
	}
	return c.Inner.Stream(ctx, req)
}

// LastRequest returns the most recent completion request.
func (c *RecordingClient) LastRequest() modelcall.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// AllRequests returns every recorded completion request in order.
func (c *RecordingClient) AllRequests() []modelcall.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]modelcall.CompletionRequest, len(c.all))
	copy(out, c.all)
	return out
}

// RequestsWhere preserves request order; a nil predicate matches every request.
func (c *RecordingClient) RequestsWhere(pred func(modelcall.CompletionRequest) bool) []modelcall.CompletionRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []modelcall.CompletionRequest
	for _, req := range c.all {
		if pred == nil || pred(req) {
			out = append(out, req)
		}
	}
	return out
}

func (c *RecordingClient) record(req modelcall.CompletionRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.last = req
	c.all = append(c.all, req)
}

// KickDrivenMock selects responses from the latest host kick.
type KickDrivenMock struct {
	cfg KickDrivenConfig
}

// KickDrivenConfig maps detected kicks to handlers; unhandled kicks use Fallback.
type KickDrivenConfig struct {
	OnPhaseAdvanced      KickHandler
	OnLegFinished        KickHandler
	OnWorkerTaskFinished KickHandler
	OnGateBlocked        KickHandler
	OnFeedbackPending    KickHandler
	OnComposeDone        KickHandler
	// Nil returns an empty completion.
	Fallback KickHandler
}

// KickHandler is a per-kick response builder.
type KickHandler func(ctx context.Context, snap Snapshot) modelcall.Completion

// Snapshot is the kick-driven prompt context provided to a handler.
type Snapshot struct {
	// Host wakes can be the latest user-role message.
	LastUserMessage string
	// Empty when the prompt contains no worker summary.
	LastWorkerSummary string
	// Durable identity lets fixtures consume each worker outcome once across wake types.
	LastWorkerJobID string
	// KickID is the detected kick id (e.g. "leg-finished", "phase-advanced").
	KickID string
}

// NewKickDrivenMock builds a kick-driven mock client.
func NewKickDrivenMock(cfg KickDrivenConfig) *KickDrivenMock {
	return &KickDrivenMock{cfg: cfg}
}

// Complete dispatches to the configured handler for the detected kick.
func (m *KickDrivenMock) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap := buildSnapshot(req.Messages)
	handler := m.selectHandler(snap.KickID)
	if handler == nil {
		return &modelcall.Completion{}, nil
	}
	out := handler(ctx, snap)
	return &out, nil
}

// Stream re-uses Complete and streams tokens (single-shot for tests).
func (m *KickDrivenMock) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	ch := make(chan modelcall.StreamChunk)
	go func() {
		defer close(ch)
		completion, err := m.Complete(ctx, req)
		if err != nil || completion == nil {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Done: true})
			return
		}
		if len(completion.ToolCalls) > 0 {
			modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{ToolCalls: completion.ToolCalls, Done: true})
			return
		}
		modelcall.SendChunk(ctx, ch, modelcall.StreamChunk{Content: completion.Content, Done: true})
	}()
	return ch, nil
}

func (m *KickDrivenMock) selectHandler(kickID string) KickHandler {
	switch kickID {
	case "phase-advanced":
		if m.cfg.OnPhaseAdvanced != nil {
			return m.cfg.OnPhaseAdvanced
		}
	case "leg-finished":
		if m.cfg.OnLegFinished != nil {
			return m.cfg.OnLegFinished
		}
	case "worker-task-finished":
		if m.cfg.OnWorkerTaskFinished != nil {
			return m.cfg.OnWorkerTaskFinished
		}
	case "gate-blocked":
		if m.cfg.OnGateBlocked != nil {
			return m.cfg.OnGateBlocked
		}
	case "feedback-pending":
		if m.cfg.OnFeedbackPending != nil {
			return m.cfg.OnFeedbackPending
		}
	case "compose-done":
		if m.cfg.OnComposeDone != nil {
			return m.cfg.OnComposeDone
		}
	}
	return m.cfg.Fallback
}

// The snapshot retains the latest summary, user message, and rendered kick.
func buildSnapshot(messages []api.Message) Snapshot {
	snap := Snapshot{}
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if snap.LastWorkerJobID == "" && msg.WorkerSummary != nil {
			snap.LastWorkerSummary = msg.Content
			snap.LastWorkerJobID = msg.WorkerSummary.WorkerID
		}
		if snap.LastUserMessage == "" && msg.Role == api.MessageRoleUser {
			snap.LastUserMessage = msg.Content
		}
		if snap.KickID == "" {
			snap.KickID = detectKickID(msg.Content)
		}
		if snap.KickID != "" && snap.LastWorkerJobID != "" && snap.LastUserMessage != "" {
			break
		}
	}
	return snap
}

// Fixture kicks use their rendered template prefixes.
func detectKickID(body string) string {
	t := strings.TrimSpace(body)
	switch {
	case strings.HasPrefix(t, "Phase advanced"):
		return "phase-advanced"
	case strings.HasPrefix(t, "Leg finished"):
		return "leg-finished"
	case strings.HasPrefix(t, "Worker task finished"):
		return "worker-task-finished"
	case strings.HasPrefix(t, "Gate blocked"):
		return "gate-blocked"
	case strings.HasPrefix(t, "User feedback is pending"):
		return "feedback-pending"
	case strings.HasPrefix(t, "Compose succeeded"):
		return "compose-done"
	case strings.HasPrefix(t, "Security scan finished"):
		return "scan-finished"
	default:
		return ""
	}
}
