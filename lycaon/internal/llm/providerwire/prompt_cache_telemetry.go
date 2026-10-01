package providerwire

import (
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/scopedstore"
)

// Diagnostic thresholds, never provider eligibility or host control decisions.
const (
	promptCacheWindowSize = 8
	promptCacheMinSamples = 3
	promptCacheMinInput   = 4096
)

type promptCacheSample struct{ input, read, written int }

type promptCacheSessionStats struct {
	standing, tools, controls, history [sha256.Size]byte
	historyCount                       int
	started, finished                  time.Time
	valid                              bool
	window                             [promptCacheWindowSize]promptCacheSample
	next, count                        int
	alert                              string
}

type promptCacheHitTracker struct {
	mu sync.Mutex
	// Bounded by session, provider, model, and request purpose.
	sessions scopedstore.LRU[*promptCacheSessionStats]
}

var globalPromptCacheHitTracker promptCacheHitTracker

func (t *promptCacheHitTracker) note(scope observability.PromptCacheScope, req modelcall.CompletionRequest, policy providerprofile.PromptCachePolicy, usage modelcall.TokenUsage, started, finished time.Time, failed bool) observability.PromptCacheObservation {
	identity := cacheRequestIdentity(req, policy)
	t.mu.Lock()
	defer t.mu.Unlock()
	st, found := t.sessions.Load(promptCacheScopeKey(scope))
	if !found {
		st = &promptCacheSessionStats{}
	}
	out := observability.PromptCacheObservation{
		PromptCacheScope: scope, CallID: req.Debug.CallID, Time: finished,
		PromptCache: policy.Mode.WireValue(), UsageReported: usage.Reported(), UsageIncomplete: usage.Incomplete,
		InputTokens: usage.PromptTokens, ReadTokens: usage.CacheReadInputTokens, WrittenTokens: usage.CacheCreationInputTokens,
	}
	if found {
		out.StandingChanged = identity.standing != st.standing
		out.ToolsChanged = identity.tools != st.tools
		out.ControlsChanged = identity.controls != st.controls
		out.HistoryChanged = st.historyCount > len(identity.history) || st.historyCount > 0 && identity.history[st.historyCount-1] != st.history
		out.IdleMS = max(int64(0), started.Sub(st.started).Milliseconds())
	}
	out.Comparison = cacheComparison(st, found, identity, policy, usage, started, failed, out)
	if out.Comparison == "comparable" {
		st.window[st.next] = promptCacheSample{usage.PromptTokens, usage.CacheReadInputTokens, usage.CacheCreationInputTokens}
		st.next = (st.next + 1) % promptCacheWindowSize
		st.count = min(st.count+1, promptCacheWindowSize)
	} else {
		st.window, st.next, st.count = [promptCacheWindowSize]promptCacheSample{}, 0, 0
	}
	out.Window = st.summary()
	out.Alert = cacheWindowAlert(out.Window)
	out.Warn = out.Alert != "" && out.Alert != st.alert
	// A late completion cannot replace the baseline of a newer request.
	if !found || !started.Before(st.started) {
		st.standing, st.tools, st.controls = identity.standing, identity.tools, identity.controls
		st.historyCount = len(identity.history)
		if st.historyCount > 0 {
			st.history = identity.history[st.historyCount-1]
		}
		st.started, st.finished = started, finished
		st.valid = identity.valid && !identity.retried && !failed && usage.Reported() && !usage.Incomplete
	}
	st.alert = out.Alert
	t.sessions.Store(promptCacheScopeKey(scope), st)
	return out
}

func cacheComparison(st *promptCacheSessionStats, found bool, identity promptCacheIdentity, policy providerprofile.PromptCachePolicy, usage modelcall.TokenUsage, started time.Time, failed bool, out observability.PromptCacheObservation) string {
	switch {
	case policy.Mode == providerprofile.PromptCacheLocalKV:
		return "local_kv"
	case !policy.Mode.Caches():
		return "uncached"
	case failed:
		return "request_failed"
	case !usage.Reported() || usage.Incomplete:
		return "usage_unavailable"
	case !identity.valid:
		return "identity_unavailable"
	case identity.retried:
		return "multiple_attempts"
	case !found:
		return "first_request"
	case started.Before(st.finished):
		return "overlapping_requests"
	case !st.valid:
		return "baseline_unavailable"
	case out.StandingChanged || out.ToolsChanged || out.ControlsChanged:
		return "prefix_changed"
	case policy.StandingColdAfter() > 0 && started.Sub(st.started) >= policy.StandingColdAfter():
		return "idle"
	case out.HistoryChanged:
		return "history_changed"
	default:
		return "comparable"
	}
}

func (st *promptCacheSessionStats) summary() observability.PromptCacheWindow {
	out := observability.PromptCacheWindow{Requests: st.count}
	for _, sample := range st.window {
		out.InputTokens += sample.input
		out.ReadTokens += sample.read
		out.WrittenTokens += sample.written
	}
	if out.InputTokens > 0 {
		out.ReadFraction = float64(out.ReadTokens) / float64(out.InputTokens)
	}
	return out
}

func cacheWindowAlert(window observability.PromptCacheWindow) string {
	if window.Requests < promptCacheMinSamples || window.InputTokens < window.Requests*promptCacheMinInput {
		return ""
	}
	if window.ReadTokens == 0 {
		return "no_reads_reported"
	}
	if window.ReadFraction < 0.1 {
		return "low_read_fraction"
	}
	return ""
}

// Local evaluation time is an observation, not evidence of a cache hit.
func NoteOllamaPromptEvalDuration(scope observability.PromptCacheScope, durationNs int64) {
	if strings.TrimSpace(scope.SessionID) == "" || durationNs <= 0 {
		return
	}
	slog.Debug("ollama prompt evaluation", "session_id", scope.SessionID, "provider_id", scope.ProviderID,
		"model", scope.Model, "purpose", scope.Purpose, "duration_ns", durationNs)
}

func promptCacheScopeKey(scope observability.PromptCacheScope) string {
	key, _ := json.Marshal([4]string{scope.SessionID, scope.ProviderID, scope.Model, scope.Purpose}) //nolint:errchkjson // String arrays always encode.
	return string(key)
}

func PromptCacheScope(providerID, model string, debug modelcall.RequestDebug) observability.PromptCacheScope {
	return observability.PromptCacheScope{SessionID: debug.SessionID, ProviderID: providerID, Model: model, Purpose: debug.Purpose}
}
