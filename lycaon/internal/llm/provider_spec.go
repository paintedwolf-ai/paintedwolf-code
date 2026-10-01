package llm

import (
	"fmt"
	"net/url"
	"strings"
)

// ProviderProtocol is a vendor wire contract defined by a provider kind. Users
// configure an endpoint or account; paths, API versions, auth headers, and wire
// semantics are host-defined protocol facts.
type ProviderProtocol string

const (
	ProtocolOpenAICompat ProviderProtocol = "openai_compatible"
	ProtocolAnthropic    ProviderProtocol = "anthropic_messages"
	ProtocolOllama       ProviderProtocol = "ollama_chat"
	ProtocolBedrock      ProviderProtocol = "bedrock_converse"
	ProtocolGeminiNative ProviderProtocol = "gemini_generate_content"
)

type ProviderKindSpec struct {
	Kind             string
	Protocol         ProviderProtocol
	ChatBaseURL      func(string) string
	ResourceRootOnly bool
}

type ResolvedProvider struct {
	Entry       CatalogEntry
	Spec        ProviderKindSpec
	APIKey      string
	ChatBaseURL string
}

func identityProviderBase(raw string) string { return strings.TrimRight(strings.TrimSpace(raw), "/") }

func azureV1Base(raw string) string {
	base := identityProviderBase(raw)
	if strings.HasSuffix(base, "/openai/v1") {
		return base
	}
	return base + "/openai/v1"
}

var providerKindSpecs = map[string]ProviderKindSpec{
	"openai":                {Kind: "openai", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"openai-compatible":     {Kind: "openai-compatible", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"azure":                 {Kind: "azure", Protocol: ProtocolOpenAICompat, ChatBaseURL: azureV1Base, ResourceRootOnly: true},
	"anthropic":             {Kind: "anthropic", Protocol: ProtocolAnthropic, ChatBaseURL: identityProviderBase},
	"ollama":                {Kind: "ollama", Protocol: ProtocolOllama, ChatBaseURL: identityProviderBase},
	"bedrock":               {Kind: "bedrock", Protocol: ProtocolBedrock, ChatBaseURL: identityProviderBase},
	"vertex":                {Kind: "vertex", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"vertex-express":        {Kind: "vertex-express", Protocol: ProtocolGeminiNative, ChatBaseURL: identityProviderBase},
	"fireworks":             {Kind: "fireworks", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"together":              {Kind: "together", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"gemini":                {Kind: "gemini", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"openrouter":            {Kind: "openrouter", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"lmstudio":              {Kind: "lmstudio", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"omlx":                  {Kind: "omlx", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"litellm-proxy":         {Kind: "litellm-proxy", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
	"cloudflare-workers-ai": {Kind: "cloudflare-workers-ai", Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase},
}

func ValidateProviderProtocolBase(kind, raw string) error {
	spec, exists := providerKindSpecs[strings.TrimSpace(kind)]
	if !exists || !spec.ResourceRootOnly {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if strings.Trim(parsed.Path, "/") != "" {
		return fmt.Errorf("%s endpoint must be a resource root without a path", spec.Kind)
	}
	return nil
}

func ResolveProvider(entry CatalogEntry, apiKey string) ResolvedProvider {
	spec, ok := providerKindSpecs[entry.Kind]
	if !ok {
		spec = ProviderKindSpec{Kind: entry.Kind, Protocol: ProtocolOpenAICompat, ChatBaseURL: identityProviderBase}
	}
	return ResolvedProvider{Entry: entry, Spec: spec, APIKey: apiKey, ChatBaseURL: spec.ChatBaseURL(entry.BaseURL)}
}
