package observability

import (
	"encoding/json"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/debugpaths"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

// LLMDebugEnabled reports whether outbound provider payloads are mirrored to a capture file.
func LLMDebugEnabled() bool {
	return debugpaths.Enabled(debugpaths.KindLLM)
}

// LLMRequestTiming captures provider round-trip latency for debug JSONL.
type LLMRequestTiming struct {
	DurationMs int64
	TTFTMs     int64
}

type llmTokenUsageCapture struct {
	Incomplete                 bool `json:"incomplete,omitempty"`
	Present                    bool `json:"present,omitempty"`
	CacheCreation1HInputTokens int  `json:"cache_creation_1h_input_tokens,omitempty"`
	PromptTokens               int  `json:"prompt_tokens,omitempty"`
	CompletionTokens           int  `json:"completion_tokens,omitempty"`
	CacheReadInputTokens       int  `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens   int  `json:"cache_creation_input_tokens,omitempty"`
}

type llmCaptureEntry struct {
	CallID            string                       `json:"call_id,omitempty"`
	ReasoningRecovery bool                         `json:"reasoning_recovery,omitempty"`
	EmptyCompletion   *LLMEmptyCompletionCapture   `json:"empty_completion,omitempty"`
	RequestControls   []map[string]json.RawMessage `json:"request_controls,omitempty"`
	Time              time.Time                    `json:"ts"`
	ProviderID        string                       `json:"provider_id"`
	Model             string                       `json:"model"`
	Call              string                       `json:"call"`
	SessionID         string                       `json:"session_id,omitempty"`
	AgentType         string                       `json:"agent_type,omitempty"`
	ParentSessionID   string                       `json:"parent_session_id,omitempty"`
	ProfileID         string                       `json:"profile_id,omitempty"`
	HostTurn          bool                         `json:"host_turn,omitempty"`
	Surface           string                       `json:"surface,omitempty"`
	Iteration         int                          `json:"iteration,omitempty"`
	MaxIterations     int                          `json:"max_iterations,omitempty"`
	WorkflowRevision  int64                        `json:"workflow_revision,omitempty"`
	ToolNames         []string                     `json:"tool_names,omitempty"`
	DurationMs        int64                        `json:"duration_ms,omitempty"`
	TTFTMs            int64                        `json:"ttft_ms,omitempty"`
	Usage             *llmTokenUsageCapture        `json:"usage,omitempty"`
	Completion        *LLMCompletionCapture        `json:"completion,omitempty"`
	Error             string                       `json:"error,omitempty"`
	Messages          []api.Message                `json:"messages"`
	Tools             []LLMToolCapture             `json:"tools,omitempty"`
}

// LLMEmptyCompletionCapture records empty-response protocol facts.
type LLMEmptyCompletionCapture struct {
	Terminal  bool   `json:"terminal"`
	Retryable bool   `json:"retryable"`
	Attempts  int    `json:"attempts"`
	Reason    string `json:"reason,omitempty"`
}

// LLMToolCapture is the tool schema recorded with a provider request.
type LLMToolCapture struct {
	Name              string         `json:"Name"`
	Description       string         `json:"Description"`
	ArgsSchema        map[string]any `json:"ArgsSchema"`
	ApprovalCategory  string         `json:"ApprovalCategory"`
	ApprovalSubject   string         `json:"ApprovalSubject"`
	UntrustedMetadata bool           `json:"UntrustedMetadata"`
	Deferred          bool           `json:"Deferred"`
	ReadOnlyHint      bool           `json:"ReadOnlyHint"`
}

// LLMCompletionToolCall summarizes one tool call in a provider response.
type LLMCompletionToolCall struct {
	Name      string `json:"name"`
	ArgsBytes int    `json:"args_bytes"`
}

// LLMCompletionCapture mirrors the visible completion shape for debug diagnosis.
type LLMCompletionCapture struct {
	ReasoningBytes          int                     `json:"reasoning_bytes,omitempty"`
	ReasoningTail           string                  `json:"reasoning_tail,omitempty"`
	ReasoningCaptureOmitted bool                    `json:"reasoning_capture_omitted,omitempty"`
	ContentChars            int                     `json:"content_chars,omitempty"`
	ContentPreview          string                  `json:"content_preview,omitempty"`
	ToolCalls               []LLMCompletionToolCall `json:"tool_calls,omitempty"`
	EstimatedVisibleTokens  int                     `json:"estimated_visible_tokens,omitempty"`
	HiddenTokenEstimate     int                     `json:"hidden_token_estimate,omitempty"`
}

var (
	// captureMu protects capture resets from active stream writers.
	captureMu       sync.RWMutex
	llmCaptureOnce  sync.Once
	llmCapture      *jsonlDebugLog
	sessionManifest *jsonlDebugLog
	manifestSeen    sync.Map // session_id -> struct{}
)

func initLLMCaptureLog() {
	if !LLMDebugEnabled() {
		return
	}
	log, err := openJSONLDebugLog(true, debugpaths.KindLLM)
	if err != nil {
		slog.Warn("llm capture disabled", "err", err)
		return
	}
	llmCapture = log
	slog.Info("llm capture enabled", "path", log.path)

	// The manifest records one topology row per session.
	manifest, err := openJSONLDebugLogAt(debugpaths.FilePath(debugpaths.KindSessions))
	if err != nil {
		slog.Warn("session manifest disabled", "err", err)
		return
	}
	sessionManifest = manifest
}

func activeLLMCaptureLog() *jsonlDebugLog {
	llmCaptureOnce.Do(func() {
		captureMu.Lock()
		defer captureMu.Unlock()
		initLLMCaptureLog()
	})
	captureMu.RLock()
	defer captureMu.RUnlock()
	return llmCapture
}

// LLMRequestDebug is optional metadata for debug JSONL rows.
type LLMRequestDebug struct {
	CallID            string
	ReasoningRecovery bool
	EmptyCompletion   *LLMEmptyCompletionCapture
	RequestControls   []map[string]json.RawMessage
	SessionID         string
	AgentType         string
	ParentSessionID   string
	ProfileID         string
	HostTurn          bool
	Surface           string
	Iteration         int
	MaxIterations     int
	// WorkflowRevision identifies the coordinator workflow snapshot.
	WorkflowRevision int64
}

// LogLLMRequest records an outbound provider call and its terminal error.
func LogLLMRequest(providerID, model, call string, messages []api.Message, toolMetas []LLMToolCapture, timing LLMRequestTiming, usage LLMUsageCapture, debug LLMRequestDebug, completion *LLMCompletionCapture, errMsg string) {
	log := activeLLMCaptureLog()
	if log == nil {
		return
	}
	entry := llmCaptureEntry{
		CallID:            debug.CallID,
		ReasoningRecovery: debug.ReasoningRecovery,
		RequestControls:   debug.RequestControls,
		EmptyCompletion:   debug.EmptyCompletion,
		Time:              time.Now().UTC(),
		ProviderID:        providerID,
		Model:             model,
		Call:              call,
		SessionID:         strings.TrimSpace(debug.SessionID),
		AgentType:         strings.TrimSpace(debug.AgentType),
		ParentSessionID:   strings.TrimSpace(debug.ParentSessionID),
		ProfileID:         strings.TrimSpace(debug.ProfileID),
		HostTurn:          debug.HostTurn,
		Surface:           strings.TrimSpace(debug.Surface),
		Iteration:         debug.Iteration,
		MaxIterations:     debug.MaxIterations,
		WorkflowRevision:  debug.WorkflowRevision,
		DurationMs:        timing.DurationMs,
		TTFTMs:            timing.TTFTMs,
		Error:             RedactCaptureText(strings.TrimSpace(errMsg)),
		Messages:          dedupeMessageBodies(RedactMessagesForCapture(messages)),
	}
	if len(toolMetas) > 0 {
		entry.ToolNames, entry.Tools = redactToolCaptures(toolMetas)
	}
	if usage.Present || usage.PromptTokens > 0 || usage.CompletionTokens > 0 || usage.CacheReadInputTokens > 0 || usage.CacheCreationInputTokens > 0 {
		entry.Usage = &llmTokenUsageCapture{
			PromptTokens:               usage.PromptTokens,
			CompletionTokens:           usage.CompletionTokens,
			CacheReadInputTokens:       usage.CacheReadInputTokens,
			CacheCreationInputTokens:   usage.CacheCreationInputTokens,
			Present:                    usage.Present,
			Incomplete:                 usage.Incomplete,
			CacheCreation1HInputTokens: usage.CacheCreation1HInputTokens,
		}
		if completion != nil {
			completion.HiddenTokenEstimate = hiddenTokenEstimate(usage.CompletionTokens, completion.EstimatedVisibleTokens)
		}
	}
	if completion != nil {
		// Screen captures assembled directly by callers before truncating them.
		redactedCompletion := *completion
		redactedCompletion.ContentPreview = RedactCaptureText(redactedCompletion.ContentPreview)
		redactedCompletion.ReasoningTail = RedactCaptureText(redactedCompletion.ReasoningTail)
		entry.Completion = &redactedCompletion
	}
	log.write(entry)
	// The manifest reads its task from redacted history.
	recordSessionManifest(entry, entry.Messages)
}

// redactToolCaptures screens credentials in tool descriptions and defaults.
func redactToolCaptures(toolMetas []LLMToolCapture) ([]string, []LLMToolCapture) {
	names := make([]string, 0, len(toolMetas))
	captured := make([]LLMToolCapture, 0, len(toolMetas))
	for _, meta := range toolMetas {
		if name := strings.TrimSpace(meta.Name); name != "" {
			names = append(names, name)
		}
		meta.Description = RedactCaptureText(meta.Description)
		if schema, ok := RedactCaptureValue(meta.ArgsSchema).(map[string]any); ok {
			meta.ArgsSchema = schema
		}
		meta.ApprovalSubject = RedactCaptureText(meta.ApprovalSubject)
		captured = append(captured, meta)
	}
	return names, captured
}

// sessionManifestEntry is the topology row for one session.
type sessionManifestEntry struct {
	Time            time.Time `json:"ts"`
	SessionID       string    `json:"session_id"`
	AgentType       string    `json:"agent_type,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	ProfileID       string    `json:"profile_id,omitempty"`
	Surface         string    `json:"surface,omitempty"`
	Task            string    `json:"task,omitempty"`
}

// recordSessionManifest writes one topology row the first time a session is seen.
func recordSessionManifest(entry llmCaptureEntry, messages []api.Message) {
	captureMu.RLock()
	defer captureMu.RUnlock()
	if sessionManifest == nil || entry.SessionID == "" {
		return
	}
	if _, seen := manifestSeen.LoadOrStore(entry.SessionID, struct{}{}); seen {
		return
	}
	sessionManifest.write(sessionManifestEntry{
		Time:            entry.Time,
		SessionID:       entry.SessionID,
		AgentType:       entry.AgentType,
		ParentSessionID: entry.ParentSessionID,
		ProfileID:       entry.ProfileID,
		Surface:         entry.Surface,
		Task:            firstUserText(messages),
	})
}

// SummarizeLLMCompletion builds a debug snapshot from visible completion output.
func SummarizeLLMCompletion(content string, toolCalls []api.ToolCall) *LLMCompletionCapture {
	if content == "" && len(toolCalls) == 0 {
		return nil
	}
	summary := &LLMCompletionCapture{
		ContentChars: len(content),
		// Truncation can hide part of a secret from the redactor.
		ContentPreview: TruncateDebugContent(RedactCaptureText(content), 2048),
	}
	visibleBytes := len(content)
	for _, tc := range toolCalls {
		name := strings.TrimSpace(tc.Name)
		argsBytes := 0
		if tc.Args != nil {
			if raw, err := json.Marshal(tc.Args); err == nil {
				argsBytes = len(raw)
			}
		}
		visibleBytes += argsBytes
		if name == "" && argsBytes == 0 {
			continue
		}
		summary.ToolCalls = append(summary.ToolCalls, LLMCompletionToolCall{
			Name:      name,
			ArgsBytes: argsBytes,
		})
	}
	summary.EstimatedVisibleTokens = tokenest.FromUnitCount(visibleBytes, tokenest.DefaultDivisor)
	return summary
}

func hiddenTokenEstimate(completionTokens, estimatedVisible int) int {
	if completionTokens <= 0 || estimatedVisible < 0 {
		return 0
	}
	if completionTokens <= estimatedVisible {
		return 0
	}
	return completionTokens - estimatedVisible
}

// TruncateDebugContent caps captured completion text for debug JSONL.
func TruncateDebugContent(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if text == "" || maxRunes <= 0 {
		return ""
	}
	return runeclamp.Clamp(text, maxRunes)
}

// firstUserText returns the truncated first user message.
func firstUserText(messages []api.Message) string {
	for _, m := range messages {
		if m.Role != api.MessageRoleUser {
			continue
		}
		if text := curationctx.TruncateTaskHint(m.Content); text != "" {
			return text
		}
	}
	return ""
}

// LLMUsageCapture is token usage for debug JSONL (includes provider prompt cache when available).
type LLMUsageCapture struct {
	Incomplete                 bool
	Present                    bool
	CacheCreation1HInputTokens int
	PromptTokens               int
	CompletionTokens           int
	CacheReadInputTokens       int
	CacheCreationInputTokens   int
}
