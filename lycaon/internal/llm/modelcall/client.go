package modelcall

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// RequestDebug carries optional request metadata.
type RequestDebug struct {
	CallID string

	SessionID string
	ProjectID string
	// ProjectDir scopes secret release leases.
	ProjectDir      string
	RootSessionID   string
	AgentType       string
	ParentSessionID string
	ProfileID       string
	HostTurn        bool
	Surface         string
	Iteration       int
	MaxIterations   int
	// WorkflowRevision identifies the coordinator workflow snapshot.
	WorkflowRevision int64
	// Purpose labels one-shot utility completions.
	Purpose string
}

// RequestComposition says who composed a provider request.
type RequestComposition string

const (
	// CompositionConversation is the coordinator's turn: a detected credential
	// in it is the person's to release or hold.
	CompositionConversation RequestComposition = ""
	// CompositionHostUtility marks host requests that have no turn available for approval cards.
	CompositionHostUtility RequestComposition = "host_utility"
)

// CompletionRequest is a chat completion request to a provider.
type CompletionRequest struct {
	StrictBudget          bool                   `json:"-" yaml:"-"`
	AttemptBudget         *CompletionBudget      `json:"-" yaml:"-"`
	ThinkingOverride      *ThinkingOverride      `json:"-" yaml:"-"`
	ThinkingOverrideStyle modelinfo.ThinkStyle   `json:"-" yaml:"-"`
	ControlCapture        *RequestControlCapture `json:"-" yaml:"-"`
	sourceCapture         *SourceRequestCapture
	Model                 string
	Messages              []api.Message
	Tools                 []tools.ToolMeta
	Debug                 RequestDebug
	// Composition says who composed this request. The zero value is the
	// coordinator conversation; utility calls stamp their own at one funnel.
	Composition RequestComposition
	// Think is the call site's reasoning directive.
	Think ThinkLevel
	// MaxTokens caps completion output when positive.
	MaxTokens int
	// ResponseFormat requests structured output.
	ResponseFormat *ResponseFormat
}

// SystemPrompt joins system messages in request order.
func (r CompletionRequest) SystemPrompt() string {
	var b strings.Builder
	for _, m := range r.Messages {
		if m.Role != api.MessageRoleSystem {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(m.Content)
	}
	return b.String()
}

// StreamChunk is one streamed completion delta.
type StreamChunk struct {
	// ResetReasoning drops failed-attempt reasoning before an internal retry.
	ResetReasoning bool `json:"-" yaml:"-"`
	// Scripted marks a host-supplied fixture response, never a provider claim.
	Scripted   bool
	Content    string
	ProviderID string
	Model      string
	Fallback   bool
	// Reasoning is a separate provider reasoning delta.
	Reasoning string
	// ReasoningDetails carries terminal structured reasoning blocks.
	ReasoningDetails []json.RawMessage
	ToolCalls        []api.ToolCall
	Usage            TokenUsage
	Done             bool
	// Progress marks an intermediate tool-call snapshot (args may be partial).
	Progress bool
	// Err is set on the terminal chunk when the provider stream failed.
	Err error
}

// Completion is an assembled model response.
type Completion struct {
	Scripted bool
	// SourceContext is host metadata from the prepared request, never provider output.
	SourceContext *api.SourceContext
	Content       string
	// Reasoning stays separate from transcript prose.
	Reasoning string
	// ReasoningDetails preserves structured reasoning bytes.
	ReasoningDetails []json.RawMessage
	ToolCalls        []api.ToolCall
	Usage            TokenUsage
	ProviderID       string
	Model            string
	Fallback         bool
}

// ModelReasoning projects replayable reasoning onto the transcript.
func (c *Completion) ModelReasoning() *api.ModelReasoning {
	if c == nil || c.Reasoning == "" && len(c.ReasoningDetails) == 0 {
		return nil
	}
	return &api.ModelReasoning{
		ProviderID: c.ProviderID,
		Model:      c.Model,
		Text:       c.Reasoning,
		Details:    c.ReasoningDetails,
	}
}

// TokenUsage stores normalized inclusive input counts.
type TokenUsage struct {
	Incomplete                 bool `json:"incomplete,omitempty"`
	Present                    bool `json:"present,omitempty"`
	CacheCreation1HInputTokens int  `json:"cache_creation_1h_input_tokens,omitempty"`
	PromptTokens               int  `json:"prompt_tokens,omitempty"`
	CompletionTokens           int  `json:"completion_tokens,omitempty"`
	CacheReadInputTokens       int  `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens   int  `json:"cache_creation_input_tokens,omitempty"`
}

// LLMClient is a facade over ProviderRegistry for session prompt loops.
type LLMClient interface {
	Complete(ctx context.Context, req CompletionRequest) (*Completion, error)
	Stream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error)
}

// CompletionHasPayload reports visible prose or tool calls.
func CompletionHasPayload(completion *Completion) bool {
	if completion == nil {
		return false
	}
	return strings.TrimSpace(completion.Content) != "" || len(completion.ToolCalls) > 0
}

// Reported is whether the provider stated this call's token use.
func (u TokenUsage) Reported() bool {
	return u.Present || u.PromptTokens > 0 || u.CompletionTokens > 0 ||
		u.CacheReadInputTokens > 0 || u.CacheCreationInputTokens > 0
}
