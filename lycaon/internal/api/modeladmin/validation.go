package modeladmin

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerauth"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// Provider labels use the project label limit.
const maxProviderDisplayLabelRunes = 64

func validateProviderBaseURL(style llm.EndpointStyle, raw string) error {
	raw = strings.TrimSpace(raw)
	switch style.Normalize() {
	case llm.EndpointStyleDerived:
		if raw != "" {
			return fmt.Errorf("base_url must be empty for a derived endpoint")
		}
		return nil
	case llm.EndpointStyleRegion:
		if !providerauth.ValidRegionIdentifier(raw) {
			return fmt.Errorf("base_url region must be a region identifier")
		}
		return nil
	case llm.EndpointStyleURL:
	default:
		return fmt.Errorf("unsupported endpoint style")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("base_url is not a valid URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return fmt.Errorf("base_url must use http or https scheme")
	}
	if u.Host == "" {
		return fmt.Errorf("base_url must include a host")
	}
	if u.User != nil {
		return fmt.Errorf("base_url must not contain user information")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("base_url must not contain a query or fragment")
	}
	return nil
}

func validateProviderDisplayLabel(label string) error {
	if utf8.RuneCountInString(label) > maxProviderDisplayLabelRunes {
		return fmt.Errorf("label must be at most %d characters", maxProviderDisplayLabelRunes)
	}
	if strings.ContainsAny(label, "\n\r\x00") {
		return fmt.Errorf("label must not contain control characters")
	}
	return nil
}

func providerCapabilitiesFromWire(in wire.ProviderModelCapabilities) (modelinfo.ModelCapabilities, error) {
	convert := func(value wire.ProviderCapabilityEvidence) (modelinfo.CapabilityEvidence, error) {
		state := modelinfo.CapabilityState(value.State)
		switch state {
		case modelinfo.CapabilityUnknown, modelinfo.CapabilitySupported, modelinfo.CapabilityUnsupported:
		default:
			return modelinfo.CapabilityEvidence{}, fmt.Errorf("invalid capability state %q", value.State)
		}
		return modelinfo.CapabilityEvidence{State: state, Sources: append([]string(nil), value.Sources...)}, nil
	}
	chat, err := convert(in.Chat)
	if err != nil {
		return modelinfo.ModelCapabilities{}, err
	}
	streaming, err := convert(in.Streaming)
	if err != nil {
		return modelinfo.ModelCapabilities{}, err
	}
	tools, err := convert(in.Tools)
	if err != nil {
		return modelinfo.ModelCapabilities{}, err
	}
	vision, err := convert(in.Vision)
	if err != nil {
		return modelinfo.ModelCapabilities{}, err
	}
	reasoning, err := convert(in.Reasoning)
	if err != nil {
		return modelinfo.ModelCapabilities{}, err
	}
	structured, err := convert(in.StructuredOutput)
	if err != nil {
		return modelinfo.ModelCapabilities{}, err
	}
	promptCaching, err := convert(in.PromptCaching)
	if err != nil {
		return modelinfo.ModelCapabilities{}, err
	}
	return modelinfo.ModelCapabilities{
		Chat: chat, Streaming: streaming, Tools: tools, Vision: vision,
		Reasoning: reasoning, StructuredOutput: structured, PromptCaching: promptCaching,
	}, nil
}

// Settings edits replace public metadata and retain controls authored in YAML.
func providerModelUpdate(update wire.ProviderModelConfig, capabilities modelinfo.ModelCapabilities, existing []modelinfo.Entry) modelinfo.Entry {
	inputPer1K := 0.0
	if update.InputPer1KNanoUSD != nil {
		inputPer1K = cost.NanoToUSD(*update.InputPer1KNanoUSD)
	}
	outputPer1K := 0.0
	if update.OutputPer1KNanoUSD != nil {
		outputPer1K = cost.NanoToUSD(*update.OutputPer1KNanoUSD)
	}
	model := modelinfo.Entry{
		ID: strings.TrimSpace(update.ID), InputPer1K: inputPer1K,
		OutputPer1K: outputPer1K, Currency: "USD",
		PricedAs: strings.TrimSpace(update.PricedAs), ContextLength: update.ContextLength,
		Capabilities: capabilities,
	}
	for _, prior := range existing {
		if prior.ID == model.ID {
			model.ThinkStyle = prior.ThinkStyle
			model.ThinkingAlwaysOn = prior.ThinkingAlwaysOn
			model.ReasoningEffort = prior.ReasoningEffort
			model.Thinking = prior.Thinking
			model.ReasoningEffortLevels = prior.ReasoningEffortLevels
			model.MaxTokens = prior.MaxTokens
			model.Temperature = prior.Temperature
			break
		}
	}
	return model
}
