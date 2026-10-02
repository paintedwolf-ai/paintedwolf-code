// Package providerprofile declares provider transport capabilities and defaults used by drivers and model selection.
package providerprofile

import (
	"fmt"
	"time"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
)

// Profile describes provider transport behavior.
type Profile struct {
	// ToolCalls describes structured tool-call support.
	ToolCalls ToolCallSupport
	// TextToolCallGrammars declares fallback grammars.
	TextToolCallGrammars TextToolCallGrammar
	// Streaming describes response delivery.
	Streaming StreamSupport
	// Thinking is the default reasoning-control style.
	Thinking modelinfo.ThinkStyle
	// ThinkingStyles lists supported styles beyond the default.
	ThinkingStyles []modelinfo.ThinkStyle
	// ReasoningEffortOff disables reasoning after an effort control is rejected.
	// Empty omits the control.
	ReasoningEffortOff string
	// Discovery selects the model-listing strategy.
	Discovery DiscoveryStrategy
	// DiscoveryProfile configures composable listing.
	DiscoveryProfile discovery.Profile
	// ModelRefusalCodes identifies structured model-level refusals.
	ModelRefusalCodes []string
	// ToolCallID selects tool-call identity handling.
	ToolCallID ToolCallIDStyle
	// ReasoningWire selects the provider's assistant-reasoning replay format.
	ReasoningWire ReasoningWireStyle
	// GoogleThoughtSignatures marks unsigned replayed tool calls as imported history.
	GoogleThoughtSignatures bool
	// SystemPreambleOnly projects one leading system slot and later host events in order.
	SystemPreambleOnly bool
	// RequireProviderParameters rejects routes that drop request parameters.
	RequireProviderParameters bool
	// PromptCache is the provider's prompt-cache policy, attached from the
	// provider catalog. Model rules refine it per request.
	PromptCache PromptCachePolicy
	// StreamResponseHeaderTimeout bounds the wait for streaming headers.
	StreamResponseHeaderTimeout time.Duration
	// CompleteResponseHeaderTimeout bounds the wait for completion headers.
	CompleteResponseHeaderTimeout time.Duration
	// UtilityCallTimeout bounds one utility call. Zero uses the default.
	UtilityCallTimeout time.Duration
	// BackgroundUtilityCallTimeout bounds low-priority projection work. Zero
	// uses UtilityCallTimeout.
	BackgroundUtilityCallTimeout time.Duration
	// UtilitySingleFlight serializes lite calls on this instance.
	UtilitySingleFlight bool
	// ToolResultImages places tool-result pixels on the chat-completions
	// transport, whose tool messages accept images only on some providers.
	ToolResultImages ToolResultImagePlacement
}

// ToolResultImagePlacement selects where a tool-result image rides.
type ToolResultImagePlacement string

const (
	// ToolResultImagesInline puts the image inside the tool result it belongs to.
	ToolResultImagesInline ToolResultImagePlacement = "inline"
	// ToolResultImagesUserTurn sends the images of a run of tool results in
	// one user message after it, each labeled as tool data with its call id.
	// It suits transports whose tool messages accept text only.
	ToolResultImagesUserTurn ToolResultImagePlacement = "user_turn"
)

// ReasoningWireStyle declares the assistant-history fields a provider accepts.
type ReasoningWireStyle string

const (
	ReasoningWireNone    ReasoningWireStyle = "none"
	ReasoningWireContent ReasoningWireStyle = "reasoning_content"
	ReasoningWireDetails ReasoningWireStyle = "reasoning_details"
)

// ToolCallSupport classifies tool-call support.
type ToolCallSupport string

const (
	// ToolCallsNative enables structured tool calls.
	ToolCallsNative ToolCallSupport = "native"
	// ToolCallsNone rejects requests that offer tools.
	ToolCallsNone ToolCallSupport = "none"
)

// SupportsToolCalls reports whether structured tool calls are supported.
func (t ToolCallSupport) SupportsToolCalls() bool { return t != ToolCallsNone }

// TextToolCallGrammar is a closed set of fallback wire grammars.
type TextToolCallGrammar uint8

const (
	// TextToolCallGrammarBoundedEnvelope accepts the envelope grammar.
	TextToolCallGrammarBoundedEnvelope TextToolCallGrammar = 1 << iota
	// TextToolCallGrammarHarmony accepts the transcript grammar.
	TextToolCallGrammarHarmony
)

func (g TextToolCallGrammar) Supports(grammar TextToolCallGrammar) bool {
	return g&grammar != 0
}

// StreamSupport classifies streaming behavior.
type StreamSupport string

const (
	StreamNative StreamSupport = "native"
	StreamFanout StreamSupport = "fanout"
)

// SupportsThinkStyle reports whether the transport can emit a style.
func (c Profile) SupportsThinkStyle(style modelinfo.ThinkStyle) bool {
	if style == modelinfo.ThinkStyleNone || style == c.Thinking {
		return true
	}
	for _, s := range c.ThinkingStyles {
		if s == style {
			return true
		}
	}
	return false
}

// ToolCallIDStyle selects tool-call identity handling across requests.
type ToolCallIDStyle string

const (
	// ToolCallIDHost uses the host identity on the wire.
	ToolCallIDHost ToolCallIDStyle = ""
	// ToolCallIDWireRoundTrip replays the emitted wire identity.
	ToolCallIDWireRoundTrip ToolCallIDStyle = "wire_round_trip"
)

// RoundTripsToolCallID reports whether wire identities are replayed.
func (c Profile) RoundTripsToolCallID() bool {
	return c.ToolCallID == ToolCallIDWireRoundTrip
}

// DiscoveryStrategy selects live model discovery.
type DiscoveryStrategy string

const (
	// DiscoveryConfigured uses DiscoveryProfile.
	DiscoveryConfigured          DiscoveryStrategy = "configured"
	DiscoveryFireworks           DiscoveryStrategy = "fireworks"
	DiscoveryCloudflareWorkersAI DiscoveryStrategy = "cloudflare_workers_ai"
	DiscoveryGemini              DiscoveryStrategy = "gemini"
	DiscoveryVertexExpress       DiscoveryStrategy = "vertex_express"
	DiscoveryAnthropic           DiscoveryStrategy = "anthropic"
	DiscoveryAzure               DiscoveryStrategy = "azure"
	DiscoveryOllama              DiscoveryStrategy = "ollama"
	DiscoveryLMStudio            DiscoveryStrategy = "lmstudio"
	DiscoveryOMLX                DiscoveryStrategy = "omlx"
	DiscoveryLiteLLM             DiscoveryStrategy = "litellm"
	DiscoveryBedrock             DiscoveryStrategy = "bedrock"
	DiscoveryVertex              DiscoveryStrategy = "vertex"
	// DiscoveryNone disables live discovery.
	DiscoveryNone DiscoveryStrategy = "none"
)

func (s ReasoningWireStyle) Validate() error {
	switch s {
	case "", ReasoningWireNone, ReasoningWireContent, ReasoningWireDetails:
		return nil
	default:
		return fmt.Errorf("unsupported reasoning_wire %q; use none, reasoning_content, or reasoning_details", s)
	}
}

func ModelRefusalCodesFor(profile Profile) []string {
	if len(profile.ModelRefusalCodes) > 0 {
		return profile.ModelRefusalCodes
	}
	return providerretry.DefaultModelRefusalCodes()
}
