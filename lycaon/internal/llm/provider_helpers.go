package llm

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"net/url"
	"os"
	"runtime"
	"strings"
	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	bedrockprovider "github.com/lycaon/lycaon/internal/llm/providers/bedrock"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	vertexexpressprovider "github.com/lycaon/lycaon/internal/llm/providers/vertexexpress"
)

// EndpointStyle describes the provider-specific meaning of base_url.
type EndpointStyle string

const (
	EndpointStyleURL     EndpointStyle = "url"
	EndpointStyleRegion  EndpointStyle = "region"
	EndpointStyleDerived EndpointStyle = "derived"
)

// Normalize applies the catalog default.
func (s EndpointStyle) Normalize() EndpointStyle {
	if s == "" {
		return EndpointStyleURL
	}
	return s
}

// Validate rejects unsupported catalog values.
func (s EndpointStyle) Validate() bool {
	switch s.Normalize() {
	case EndpointStyleURL, EndpointStyleRegion, EndpointStyleDerived:
		return true
	default:
		return false
	}
}

// hydrateProviderModels merges live-discovered model metadata into a provider
// instance so request controls (thinking, context) match List() output.
func hydrateProviderModels(p modelcall.Provider, effective []modelinfo.Entry) modelcall.Provider {
	if lp, ok := p.(*loggingProvider); ok {
		next := *lp
		next.inner = hydrateProviderModels(lp.inner, effective)
		return &next
	}
	if op, ok := p.(*openaicompat.Provider); ok {
		return op.WithEffectiveModels(effective)
	}
	if ap, ok := p.(*anthropicprovider.Provider); ok {
		return ap.WithEffectiveModels(effective)
	}
	if op, ok := p.(*ollamaprovider.Provider); ok {
		return op.WithEffectiveModels(effective)
	}
	if bp, ok := p.(*bedrockprovider.Provider); ok {
		return bp.WithEffectiveModels(effective)
	}
	if vp, ok := p.(*vertexexpressprovider.Provider); ok {
		return vp.WithEffectiveModels(effective)
	}
	if cf, ok := p.(*openaicompat.CloudflareProvider); ok {
		return cf.WithEffectiveModels(effective)
	}
	return p
}

type ProviderInUseError struct {
	ProviderID string
	Scopes     []string
}

type ProviderNotFoundError struct{ ProviderID string }

func (e *ProviderNotFoundError) Error() string {
	return fmt.Sprintf("provider %q not found", e.ProviderID)
}

func (e *ProviderInUseError) Error() string {
	return fmt.Sprintf("provider %q is assigned in %s", e.ProviderID, strings.Join(e.Scopes, ", "))
}

func policyReferencesProvider(policy ModelPolicy, providerID string) bool {
	if policy.Coordinator.ProviderID == providerID || policy.Lite.ProviderID == providerID {
		return true
	}
	for _, ref := range policy.AgentPool.Models {
		if ref.ProviderID == providerID {
			return true
		}
	}
	return false
}

// ProviderReferenceScopes returns known policy layers using an instance.
func (s *PolicyStore) ProviderReferenceScopes(providerID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var scopes []string
	if policyReferencesProvider(s.bundled, providerID) {
		scopes = append(scopes, "bundled policy")
	}
	if s.global != nil && policyReferencesProvider(*s.global, providerID) {
		scopes = append(scopes, "global policy")
	}
	for projectDir, policy := range s.projectCache {
		if policyReferencesProvider(policy, providerID) {
			scopes = append(scopes, "project "+projectDir)
		}
	}
	return scopes
}

// Platform tokens are shared by provider configuration and the wire contract.
const (
	PlatformmacOS   = "macos"
	PlatformWindows = "windows"
	PlatformLinux   = "linux"
)

// hostProductPlatform maps runtime.GOOS to a product platform token.
// Overridable in tests via SetHostProductPlatformForTest.
var hostProductPlatform = defaultHostProductPlatform

func defaultHostProductPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return PlatformmacOS
	case "windows":
		return PlatformWindows
	case "linux":
		return PlatformLinux
	default:
		return runtime.GOOS
	}
}

// SetHostProductPlatformForTest overrides the resolver during tests.
func SetHostProductPlatformForTest(platform string) (restore func()) {
	if os.Getenv("LYCAON_TEST") != "1" {
		panic("llm.SetHostProductPlatformForTest requires LYCAON_TEST=1")
	}
	prev := hostProductPlatform
	hostProductPlatform = func() string { return platform }
	return func() { hostProductPlatform = prev }
}

// HostProductPlatform returns the sidecar host's product platform token.
func HostProductPlatform() string {
	return hostProductPlatform()
}

// PlatformsSupported treats an empty list as unrestricted.
func PlatformsSupported(platforms []string, host string) bool {
	if len(platforms) == 0 {
		return true
	}
	host = strings.TrimSpace(host)
	for _, p := range platforms {
		if strings.TrimSpace(p) == host {
			return true
		}
	}
	return false
}

// PlatformsSupportedHere checks the current host.
func PlatformsSupportedHere(platforms []string) bool {
	return PlatformsSupported(platforms, HostProductPlatform())
}

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
