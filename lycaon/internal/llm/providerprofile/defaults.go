package providerprofile

import (
	"time"

	"github.com/lycaon/lycaon/internal/llm/discovery"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

// OpenAIReasoningEffortOff is the OpenAI wire value that disables reasoning.
const OpenAIReasoningEffortOff = "none"

const LocalInferenceUtilityCallTimeout = 5 * time.Minute

const LocalInferenceBackgroundUtilityCallTimeout = 15 * time.Minute

const LocalInferenceStreamResponseHeaderTimeout = 5 * time.Minute

// HostedCompleteResponseHeaderTimeout bounds a hosted non-streaming completion,
// whose response headers arrive only once generation finishes.
const HostedCompleteResponseHeaderTimeout = 10 * time.Minute

// LocalInferenceCompleteResponseHeaderTimeout exceeds the background budget.
const LocalInferenceCompleteResponseHeaderTimeout = 20 * time.Minute

// Default returns conservative transport defaults.
func Default() Profile {
	return OpenAI()
}

func OpenAI() Profile {
	return Profile{
		ToolCalls:            ToolCallsNative,
		TextToolCallGrammars: TextToolCallGrammarHarmony | TextToolCallGrammarBoundedEnvelope,
		Streaming:            StreamNative,
		Thinking:             modelinfo.ThinkStyleEffortLevels,
		// Model rules may select thinking.type.
		ThinkingStyles:   []modelinfo.ThinkStyle{modelinfo.ThinkStyleThinkingType},
		Discovery:        DiscoveryConfigured,
		DiscoveryProfile: discovery.OpenAIProfile(),
		// Chat-completions tool messages take text parts only.
		ToolResultImages: ToolResultImagesUserTurn,
	}
}

func OpenAIVendor() Profile {
	profile := OpenAI()
	profile.ReasoningEffortOff = OpenAIReasoningEffortOff
	return profile
}

func Ollama() Profile {
	return LocalInference(Profile{
		ToolCalls:            ToolCallsNative,
		TextToolCallGrammars: TextToolCallGrammarBoundedEnvelope,
		Streaming:            StreamNative,
		Thinking:             modelinfo.ThinkStyleBooleanThink,
		ThinkingStyles:       []modelinfo.ThinkStyle{modelinfo.ThinkStyleEffortLevels},
		Discovery:            DiscoveryOllama,
	})
}

func Fireworks() Profile {
	profile := OpenAI()
	profile.Discovery = DiscoveryFireworks
	// Fireworks tool messages accept image parts.
	profile.ToolResultImages = ToolResultImagesInline
	return profile
}

func Together() Profile {
	profile := OpenAI()
	profile.SystemPreambleOnly = true
	profile.DiscoveryProfile = discovery.TogetherProfile()
	// Hybrid models use an on/off object; effort-based models keep their levels.
	profile.Thinking = modelinfo.ThinkStyleReasoningToggle
	profile.ThinkingStyles = []modelinfo.ThinkStyle{modelinfo.ThinkStyleEffortLevels}
	return profile
}

func Gemini() Profile {
	profile := OpenAI()
	profile.ReasoningEffortOff = OpenAIReasoningEffortOff
	profile.GoogleThoughtSignatures = true
	profile.SystemPreambleOnly = true
	profile.Discovery = DiscoveryGemini
	return profile
}

func CloudflareWorkersAI() Profile {
	profile := OpenAI()
	profile.Discovery = DiscoveryCloudflareWorkersAI
	return profile
}

func Openrouter() Profile {
	profile := OpenAI()
	profile.DiscoveryProfile = discovery.OpenRouterProfile()
	profile.Thinking = modelinfo.ThinkStyleReasoningObject
	profile.ThinkingStyles = nil
	profile.RequireProviderParameters = true
	return profile
}

func Vertex() Profile {
	profile := OpenAI()
	profile.SystemPreambleOnly = true
	profile.Discovery = DiscoveryVertex
	return profile
}

func VertexExpress() Profile {
	return Profile{
		ToolCalls:            ToolCallsNative,
		TextToolCallGrammars: TextToolCallGrammarBoundedEnvelope,
		Streaming:            StreamNative,
		Thinking:             modelinfo.ThinkStyleBudgetTokens,
		ThinkingStyles:       []modelinfo.ThinkStyle{modelinfo.ThinkStyleEffortLevels},
		Discovery:            DiscoveryVertexExpress,
		ToolCallID:           ToolCallIDHost,
		// Symbolic status identifies this model refusal.
		ModelRefusalCodes: []string{"NOT_FOUND"},
	}
}

func Azure() Profile {
	profile := OpenAIVendor()
	profile.Discovery = DiscoveryAzure
	return profile
}

func Lmstudio() Profile {
	profile := OpenAI()
	profile.Discovery = DiscoveryLMStudio
	return LocalInference(profile)
}

func Omlx() Profile {
	profile := OpenAI()
	profile.Discovery = DiscoveryOMLX
	return LocalInference(profile)
}

func Litellm() Profile {
	profile := OpenAI()
	profile.Discovery = DiscoveryLiteLLM
	return LocalInference(profile)
}

func LocalInference(profile Profile) Profile {
	profile.SystemPreambleOnly = true
	profile.StreamResponseHeaderTimeout = LocalInferenceStreamResponseHeaderTimeout
	profile.CompleteResponseHeaderTimeout = LocalInferenceCompleteResponseHeaderTimeout
	profile.UtilityCallTimeout = LocalInferenceUtilityCallTimeout
	profile.BackgroundUtilityCallTimeout = LocalInferenceBackgroundUtilityCallTimeout
	profile.UtilitySingleFlight = true
	return profile
}

func Anthropic() Profile {
	return Profile{
		ToolCalls:            ToolCallsNative,
		TextToolCallGrammars: TextToolCallGrammarBoundedEnvelope,
		Streaming:            StreamNative,
		Thinking:             modelinfo.ThinkStyleBudgetTokens,
		ThinkingStyles:       []modelinfo.ThinkStyle{modelinfo.ThinkStyleAdaptive},
		Discovery:            DiscoveryAnthropic,
		ToolCallID:           ToolCallIDWireRoundTrip,
		// A messages-endpoint 404 identifies a model refusal.
		ModelRefusalCodes: []string{"not_found_error"},
	}
}

// DefaultAnthropicMaxTokens caps a completion when the model declares no limit.
const DefaultAnthropicMaxTokens = 8192
