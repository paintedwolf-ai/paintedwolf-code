package modelcall

import (
	"reflect"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

// CompletionBudget records the controls an adapter actually applied to one
// attempt. It is shared only until that attempt's terminal result is received.
type CompletionBudget struct {
	ReducedReasoning   bool `json:"-" yaml:"-"`
	MaxTokens          int  `json:"-" yaml:"-"`
	CanReduceReasoning bool `json:"-" yaml:"-"`
}

func (req CompletionRequest) ReduceReasoning() bool {
	return req.ThinkingOverride == nil && (req.ReasoningRecovery || SessionReasoningRecovery(req.Debug.SessionID))
}

func (req CompletionRequest) RecordBudget(maxTokens int, reasoning, reduced any) {
	if req.AttemptBudget == nil {
		return
	}
	req.AttemptBudget.MaxTokens = maxTokens
	req.AttemptBudget.ReducedReasoning = req.ReduceReasoning()
	req.AttemptBudget.CanReduceReasoning = req.ThinkingOverride == nil && !req.ReduceReasoning() && !reflect.DeepEqual(reasoning, reduced)
}

// Tool requests reserve answer room plus a bounded reasoning allowance. These
// APIs share one output counter; this does not promise a separate visible cap.
// Explicit request caps and known model output limits remain hard ceilings.
func CompletionTokenLimit(req CompletionRequest, catalog, fallback int, level ThinkLevel, reasoning bool) int {
	limit := catalog
	if limit <= 0 {
		limit = fallback
		if reasoning && fallback > 0 {
			allowance, _ := level.AnthropicBudget()
			limit += allowance
		}
	}
	if ClassifyTurn(req) == TurnClassOrchestration {
		room := OrchestrationMaxTokens
		if reasoning {
			allowance, _ := level.AnthropicBudget()
			room += allowance
		}
		if catalog <= 0 || limit > room {
			limit = room
		}
	}
	if req.MaxTokens > 0 {
		limit = req.MaxTokens
	}
	if catalog > 0 && (limit <= 0 || limit > catalog) {
		limit = catalog
	}
	return limit
}

func OutputTruncation(req CompletionRequest, provider, model, reason string, usage TokenUsage, tools bool) error {
	// Utility callers require a complete title, summary, or structured result;
	// conversation turns recover through continuation and argument repair.
	if req.Composition != CompositionHostUtility && req.ResponseFormat == nil {
		return nil
	}
	if tools || !(&failure.ProviderEmptyCompletionError{Terminal: true, Reason: reason}).OutputLimitReached() {
		return nil
	}
	return &failure.ProviderOutputTruncatedError{ProviderID: provider, Model: model, FinishReason: reason, PromptTokens: usage.PromptTokens, CompletionTokens: usage.CompletionTokens}
}

func CapOutputTokens(limit, explicit, catalog int) int {
	for _, cap := range []int{explicit, catalog} {
		if cap > 0 && (limit <= 0 || limit > cap) {
			limit = cap
		}
	}
	return limit
}

// Session recovery lowers reasoning on the next attempt without changing answer limits.
var sessionReasoningRecovery = scopedstore.New[struct{}](scopedstore.DefaultEntries)

// MarkSessionReasoningRecovery lowers reasoning after excessive hidden reasoning.
func MarkSessionReasoningRecovery(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	sessionReasoningRecovery.Store(sessionID, struct{}{})
}

// SessionReasoningRecovery reports whether a prior reply reduced reasoning.
func SessionReasoningRecovery(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	_, ok := sessionReasoningRecovery.Load(sessionID)
	return ok
}

// NoteCompletionTurnBudget updates closed-loop state from a finished turn.
func NoteCompletionTurnBudget(sessionID string, maxTokens, completionTokens, estimatedVisibleTokens int) {
	if maxTokens <= 0 || completionTokens <= 0 || estimatedVisibleTokens < 0 {
		return
	}
	hidden := completionTokens - estimatedVisibleTokens
	if hidden < maxTokens-maxTokens/4 {
		return
	}
	MarkSessionReasoningRecovery(sessionID)
}

// ResetTurnBudgetForTest clears recovery state without replacing the shared store.
func ResetTurnBudgetForTest() {
	sessionReasoningRecovery.Clear()
}

// ClearSessionReasoningRecovery restores reasoning after a useful response.
func ClearSessionReasoningRecovery(sessionID string) { sessionReasoningRecovery.Delete(sessionID) }

// TurnClass groups completion requests for request-control policy.
type TurnClass string

const (
	// TurnClassOrchestration covers tool-heavy dispatch, investigate, promote, and worker turns.
	TurnClassOrchestration TurnClass = "orchestration"
	// TurnClassOpen covers synthesis, routing, and other user-facing prose turns.
	TurnClassOpen TurnClass = "open"
)

const (
	MinimumReasoningEffort = "low"
	// Base answer room for tool-driven turns, before reasoning allowance.
	OrchestrationMaxTokens = 16384
)

var openTurnSurfaces = map[string]struct{}{
	"implement_routing":   {},
	"implement_synthesis": {},
}

// ClassifyTurn derives the turn class from tool presence and coordinator surface.
func ClassifyTurn(req CompletionRequest) TurnClass {
	if len(req.Tools) == 0 {
		return TurnClassOpen
	}
	surface := strings.TrimSpace(req.Debug.Surface)
	if surface == "" {
		return TurnClassOrchestration
	}
	if _, ok := openTurnSurfaces[surface]; ok {
		return TurnClassOpen
	}
	return TurnClassOrchestration
}
