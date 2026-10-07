// Package egressclass defines app-level network purposes.
package egressclass

import (
	"fmt"
	"slices"
)

// ID identifies why the app is making an outbound request.
type ID string

const (
	LLMProviderRequest     ID = "llm_provider_request"
	WebResearchRequest     ID = "web_research_request"
	AgentHTTPRequest       ID = "agent_http_request"
	ProviderModelDiscovery ID = "provider_model_discovery"
	ModelMetadataRefresh   ID = "model_metadata_refresh"
	UpdateManifestCheck    ID = "update_manifest_check"
	MCPRemoteHTTP          ID = "mcp_remote_http"
	BrowserEngineDownload  ID = "browser_engine_download"
	DecideModelDownload    ID = "decide_model_download"
	ExtensionPackGit       ID = "extension_pack_git"
	OSVAdvisoryDownload    ID = "osv_advisory_download"
	PricingFeedRefresh     ID = "pricing_feed_refresh"
	PackageIdentityLookup  ID = "package_identity_lookup"
)

const (
	ModelMetadataEndpoint          = "https://models.dev/api.json"
	UpdateManifestEndpointTemplate = "https://downloads.paintedwolf.dev/updates/{channel}/key-{key_generation}/latest.json"
	OSVAdvisoryEndpoint            = "https://osv-vulnerabilities.storage.googleapis.com"
	DecideModelEndpoint            = "https://huggingface.co"
	PackageIdentityEndpoint        = "https://api.deps.dev"
)

// EndpointSource records who chooses the destination.
type EndpointSource string

const (
	EndpointBundled        EndpointSource = "bundled"
	EndpointUserConfigured EndpointSource = "user_configured"
	EndpointMixed          EndpointSource = "bundled_or_user_configured"
)

// Transport records the mechanism that performs the network operation.
type Transport string

const (
	HTTPBounded     Transport = "http_bounded"
	HTTPStreaming   Transport = "http_streaming"
	HTTPDownload    Transport = "http_download"
	GitCLI          Transport = "git_cli"
	LibraryDownload Transport = "library_download"
)

// Class is one app-level outbound request purpose.
type Class struct {
	ID               ID
	EndpointSource   EndpointSource
	Transport        Transport
	FixedEndpoints   []string
	EndpointTemplate string
	NoticeCode       string
	SilentReason     string
	UserInitiated    bool
}

var classes = []Class{
	{
		ID: LLMProviderRequest, EndpointSource: EndpointMixed, Transport: HTTPStreaming,
		NoticeCode: "provider_unreachable", UserInitiated: true,
	},
	{
		ID: WebResearchRequest, EndpointSource: EndpointMixed, Transport: HTTPStreaming,
		SilentReason: "web research returns a typed tool failure to the session that initiated it", UserInitiated: true,
	},
	{
		ID: AgentHTTPRequest, EndpointSource: EndpointUserConfigured, Transport: HTTPBounded,
		SilentReason: "agent HTTP requests return a typed tool failure to the initiating session", UserInitiated: true,
	},
	{
		ID: ProviderModelDiscovery, EndpointSource: EndpointMixed, Transport: HTTPBounded,
		NoticeCode: "model_catalog_unreachable", UserInitiated: true,
	},
	{
		ID: ModelMetadataRefresh, EndpointSource: EndpointBundled, Transport: HTTPBounded,
		FixedEndpoints: []string{ModelMetadataEndpoint},
		SilentReason:   "background metadata refresh keeps the prior cache; explicit provider refresh has its own typed error",
	},
	{
		ID: UpdateManifestCheck, EndpointSource: EndpointBundled, Transport: HTTPBounded,
		EndpointTemplate: UpdateManifestEndpointTemplate,
		SilentReason:     "background update checks are opt-out and manual checks report in the updates surface",
	},
	{
		ID: MCPRemoteHTTP, EndpointSource: EndpointUserConfigured, Transport: HTTPBounded,
		NoticeCode: "mcp_provider_unreachable", UserInitiated: true,
	},
	{
		ID: BrowserEngineDownload, EndpointSource: EndpointBundled, Transport: HTTPDownload,
		SilentReason: "browser provisioning reports BROWSER_ENGINE_UNAVAILABLE in readiness and tool results", UserInitiated: true,
	},
	{
		ID: DecideModelDownload, EndpointSource: EndpointBundled, Transport: HTTPDownload,
		FixedEndpoints: []string{DecideModelEndpoint},
		SilentReason:   "checkpoint provisioning reports through the decision_engine preflight probe", UserInitiated: false,
	},
	{
		ID: ExtensionPackGit, EndpointSource: EndpointUserConfigured, Transport: GitCLI,
		SilentReason: "extension install reports the git operation failure without classifying stderr prose", UserInitiated: true,
	},
	{
		ID: OSVAdvisoryDownload, EndpointSource: EndpointBundled, Transport: LibraryDownload,
		FixedEndpoints: []string{OSVAdvisoryEndpoint},
		SilentReason:   "advisory download failures are recorded as the dependency scan outcome", UserInitiated: true,
	},
	{
		ID: PricingFeedRefresh, EndpointSource: EndpointBundled, Transport: HTTPBounded,
		SilentReason: "background refresh keeps prior rates; explicit refresh returns PRICING_SOURCE_UNREACHABLE",
	},
	{
		ID: PackageIdentityLookup, EndpointSource: EndpointBundled, Transport: HTTPBounded,
		FixedEndpoints: []string{PackageIdentityEndpoint},
		SilentReason:   "the approval card reports package identity as unavailable and keeps the reduced execution boundary",
		UserInitiated:  true,
	},
}

// All returns a copy of the closed inventory.
func All() []Class {
	cloned := slices.Clone(classes)
	for index := range cloned {
		cloned[index].FixedEndpoints = slices.Clone(cloned[index].FixedEndpoints)
	}
	return cloned
}

// Lookup returns the declaration for id.
func Lookup(id ID) (Class, bool) {
	for _, class := range classes {
		if class.ID == id {
			class.FixedEndpoints = slices.Clone(class.FixedEndpoints)
			return class, true
		}
	}
	return Class{}, false
}

// RequireTransport panics unless id declares transport.
func RequireTransport(id ID, transport Transport) Class {
	class, ok := Lookup(id)
	if !ok {
		panic(fmt.Sprintf("unknown egress class %q", id))
	}
	if class.Transport != transport {
		panic(fmt.Sprintf("egress class %q uses %s, not %s", id, class.Transport, transport))
	}
	return class
}

// RequireSingleFixedEndpoint returns the sole bundled endpoint for an operation.
func RequireSingleFixedEndpoint(id ID, transport Transport) string {
	class := RequireTransport(id, transport)
	if len(class.FixedEndpoints) != 1 {
		panic(fmt.Sprintf("egress class %q has %d fixed endpoints; want one", id, len(class.FixedEndpoints)))
	}
	return class.FixedEndpoints[0]
}
