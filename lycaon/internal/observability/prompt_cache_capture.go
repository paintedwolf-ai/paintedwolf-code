package observability

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/debugpaths"
)

// PromptCacheScope separates independent prompt prefixes within a session.
type PromptCacheScope struct {
	SessionID  string `json:"session_id"`
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
	Purpose    string `json:"purpose,omitempty"`
}

// PromptCacheWindow measures recent comparable requests, excluding cold starts.
type PromptCacheWindow struct {
	Requests      int     `json:"requests"`
	InputTokens   int     `json:"input_tokens"`
	ReadTokens    int     `json:"read_tokens"`
	WrittenTokens int     `json:"written_tokens"`
	ReadFraction  float64 `json:"read_fraction"`
}

// PromptCacheObservation contains counts and comparisons, never prompt content.
// Comparison describes host projection changes, not a provider-confirmed miss.
type PromptCacheObservation struct {
	PromptCacheScope
	Time            time.Time         `json:"ts"`
	CallID          string            `json:"call_id,omitempty"`
	PromptCache     string            `json:"prompt_cache"`
	Comparison      string            `json:"comparison"`
	StandingChanged bool              `json:"standing_changed,omitempty"`
	ToolsChanged    bool              `json:"tools_changed,omitempty"`
	ControlsChanged bool              `json:"controls_changed,omitempty"`
	HistoryChanged  bool              `json:"history_changed,omitempty"`
	IdleMS          int64             `json:"idle_ms,omitempty"`
	UsageReported   bool              `json:"usage_reported"`
	UsageIncomplete bool              `json:"usage_incomplete,omitempty"`
	InputTokens     int               `json:"input_tokens"`
	ReadTokens      int               `json:"read_tokens"`
	WrittenTokens   int               `json:"written_tokens"`
	Window          PromptCacheWindow `json:"window"`
	Alert           string            `json:"alert,omitempty"`
	Warn            bool              `json:"-"`
}

var (
	promptCacheObsOnce sync.Once
	promptCacheObsLog  *jsonlDebugLog
)

func initPromptCacheObservabilityLog() {
	if !LLMDebugEnabled() || llmCapture == nil {
		return
	}
	log, err := openJSONLDebugLogAt(debugpaths.FilePath(debugpaths.KindPromptCache))
	if err != nil {
		slog.Warn("prompt cache observability capture disabled", "err", err)
		return
	}
	promptCacheObsLog = log
}

func activePromptCacheObservabilityLog() *jsonlDebugLog {
	_ = activeLLMCaptureLog()
	promptCacheObsOnce.Do(func() {
		captureMu.Lock()
		defer captureMu.Unlock()
		initPromptCacheObservabilityLog()
	})
	captureMu.RLock()
	defer captureMu.RUnlock()
	return promptCacheObsLog
}

// LogPromptCacheObservability captures every observation in debug mode and
// warns once when a comparable window enters an alert state.
func LogPromptCacheObservability(entry PromptCacheObservation) {
	entry.SessionID = strings.TrimSpace(entry.SessionID)
	if entry.SessionID == "" {
		return
	}
	if entry.Warn {
		slog.Warn("prompt cache reads remain low across comparable requests", "session_id", entry.SessionID,
			"provider_id", entry.ProviderID, "model", entry.Model, "purpose", entry.Purpose,
			"call_id", entry.CallID, "prompt_cache", entry.PromptCache, "alert", entry.Alert,
			"requests", entry.Window.Requests, "input_tokens", entry.Window.InputTokens,
			"read_tokens", entry.Window.ReadTokens, "read_fraction", entry.Window.ReadFraction)
	}
	log := activePromptCacheObservabilityLog()
	if log != nil {
		log.write(entry)
	}
}
