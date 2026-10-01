// Package providerwire prepares shared message, image, tool, and cache projections for provider transports.
package providerwire

import (
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
)

// Explicit breakpoints a request may carry beside each transport's automatic
// one. Anthropic and OpenAI allow four cache writes per request and place one
// themselves; Bedrock Converse takes four checkpoints and places none.
const (
	AnthropicExplicitBreakpointLimit = 3
	OpenAIExplicitBreakpointLimit    = 3
	BedrockCacheCheckpointLimit      = 4
)

// PromptCacheBreakpoint is one host-marked boundary a request caches up to.
type PromptCacheBreakpoint struct {
	// Index is the marked message's position in the request's messages.
	Index int
	Tier  api.PromptCacheTier
	// Lifetime is the lifetime the request sets on the boundary; zero leaves
	// the provider's default.
	Lifetime time.Duration
}

// PromptCacheProjection describes prompt-cache wire mutations.
type PromptCacheProjection struct {
	// Breakpoints lists the marked boundaries in message order.
	Breakpoints []PromptCacheBreakpoint
	// Marker is the per-part marker an automatic-prefix request places on
	// each boundary; explicit-breakpoint transports use their own.
	Marker providerprofile.PromptCacheMarker
	// RequestMarker enables request-level caching, with RequestLifetime.
	RequestMarker   bool
	RequestLifetime time.Duration
	// PromptCacheKey identifies a reusable prefix. Empty omits it.
	PromptCacheKey string
	// PromptCacheRetention requests a cache lifetime when set.
	PromptCacheRetention string
	// SessionAffinityHeader and SessionAffinityValue carry optional affinity.
	SessionAffinityHeader string
	SessionAffinityValue  string
	// KeepAlive controls local model residency.
	KeepAlive string
}

// ProjectPromptCache derives the cache controls one request sends from the
// provider's policy, refined for the routed model, and the model's catalog
// capabilities.
func ProjectPromptCache(req modelcall.CompletionRequest, policy providerprofile.PromptCachePolicy, caps modelinfo.ModelCapabilities) PromptCacheProjection {
	policy = policy.ForModel(req.Model)
	switch policy.Mode {
	case providerprofile.PromptCacheExplicitBreakpoints:
		return PromptCacheProjection{
			Breakpoints:     promptCacheBreakpoints(req.Messages, policy.Lifetime),
			RequestMarker:   true,
			RequestLifetime: policy.Lifetime.History.Duration(),
		}
	case providerprofile.PromptCacheAutomaticPrefix:
		var proj PromptCacheProjection
		if key := strings.TrimSpace(req.Debug.SessionID); key != "" {
			if policy.SessionKey {
				proj.PromptCacheKey = key
			}
			if h := strings.TrimSpace(policy.AffinityHeader); h != "" {
				proj.SessionAffinityHeader = h
				proj.SessionAffinityValue = key
			}
		}
		marker := policy.Marker
		if marker == providerprofile.PromptCacheMarkerCatalog {
			marker = providerprofile.PromptCacheMarkerNone
			if modelinfo.Supported(caps.PromptCaching) {
				marker = providerprofile.PromptCacheMarkerCacheControl
			}
		}
		if marker != providerprofile.PromptCacheMarkerNone {
			proj.Marker = marker
			proj.RequestMarker = policy.RequestMarker
			proj.Breakpoints = promptCacheBreakpoints(req.Messages, providerprofile.PromptCacheLifetime{})
		}
		proj.PromptCacheRetention = policy.Retention
		return proj
	case providerprofile.PromptCacheLocalKV:
		return PromptCacheProjection{KeepAlive: policy.KeepAlive}
	default:
		return PromptCacheProjection{}
	}
}

// promptCacheBreakpoints lists the marked boundaries in order with the
// lifetime each tier takes. Providers call it on the messages they project,
// after any transform, so an index never drifts from its marked row.
func promptCacheBreakpoints(messages []api.Message, lifetime providerprofile.PromptCacheLifetime) []PromptCacheBreakpoint {
	var out []PromptCacheBreakpoint
	for i, m := range messages {
		if m.PromptCacheBreakpoint != api.PromptCacheTierNone {
			out = append(out, PromptCacheBreakpoint{Index: i, Tier: m.PromptCacheBreakpoint, Lifetime: lifetime.For(m.PromptCacheBreakpoint)})
		}
	}
	return out
}

// TrailingPromptCacheBreakpoints keeps the last max boundaries, which are the
// ones closest to the growing tail, when a transport allows fewer markers
// than the host placed.
func TrailingPromptCacheBreakpoints(breakpoints []PromptCacheBreakpoint, max int) []PromptCacheBreakpoint {
	if max <= 0 {
		return nil
	}
	if len(breakpoints) <= max {
		return breakpoints
	}
	return breakpoints[len(breakpoints)-max:]
}

// PromptCacheBreakpointSet reports each boundary by message index.
func PromptCacheBreakpointSet(breakpoints []PromptCacheBreakpoint) map[int]PromptCacheBreakpoint {
	set := make(map[int]PromptCacheBreakpoint, len(breakpoints))
	for _, bp := range breakpoints {
		if bp.Index >= 0 {
			set[bp.Index] = bp
		}
	}
	return set
}

// LifetimeToken spells a breakpoint lifetime the way explicit-breakpoint
// transports take it ("5m", "1h"); zero leaves the provider's default.
func LifetimeToken(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d%time.Hour == 0:
		return strings.TrimSuffix(d.String(), "0m0s")
	case d%time.Minute == 0:
		return strings.TrimSuffix(d.String(), "0s")
	default:
		return d.String()
	}
}

// NotePromptCacheUsage compares bounded, independent request scopes. Timing
// and failures prevent concurrent, expired, or incomplete calls being compared.
func NotePromptCacheUsage(providerID string, req modelcall.CompletionRequest, profile providerprofile.Profile, usage modelcall.TokenUsage, started time.Time, failed bool) {
	if strings.TrimSpace(req.Debug.SessionID) == "" {
		return
	}
	scope := PromptCacheScope(providerID, req.Model, req.Debug)
	policy := profile.PromptCache.ForModel(req.Model)
	observation := globalPromptCacheHitTracker.note(scope, req, policy, usage, started, time.Now().UTC(), failed)
	observability.LogPromptCacheObservability(observation)
}
