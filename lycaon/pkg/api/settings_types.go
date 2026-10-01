package api

// --- Extensibility: MCP ---

// McpProviderStatus is the closed lifecycle enum.
type McpProviderStatus string

const (
	McpStatusDisabled   McpProviderStatus = "disabled"
	McpStatusNeedsAuth  McpProviderStatus = "needs_auth"
	McpStatusConnecting McpProviderStatus = "connecting"
	McpStatusReady      McpProviderStatus = "ready"
	McpStatusError      McpProviderStatus = "error"
	McpStatusRejected   McpProviderStatus = "rejected"
)

// McpProviderClass is host-derived local vs web.
type McpProviderClass string

const (
	McpProviderClassLocal McpProviderClass = "local"
	McpProviderClassWeb   McpProviderClass = "web"
)

// McpRecipeAuth is the credential chrome a bundled MCP recipe declares.
type McpRecipeAuth string

const (
	McpRecipeAuthStaticToken McpRecipeAuth = "static_token"
	McpRecipeAuthOAuth       McpRecipeAuth = "oauth"
	McpRecipeAuthNone        McpRecipeAuth = "none"
)

// McpCredentialWire is how a stored static token is placed on the HTTP request.
type McpCredentialWire string

const (
	McpCredentialWireBearer     McpCredentialWire = "bearer"
	McpCredentialWireTokenToken McpCredentialWire = "token_token"
	McpCredentialWireHeader     McpCredentialWire = "header"
)

type ModelRefDTO struct {
	ProviderID string `json:"provider_id"`
	Model      string `json:"model"`
}

type AgentPoolDTO struct {
	Selection string        `json:"selection"`
	Models    []ModelRefDTO `json:"models"`
}

// --- Settings: approvals & limits ---

type SettingsScope string

const (
	SettingsScopeGlobal  SettingsScope = "global"
	SettingsScopeProject SettingsScope = "project"
)

type ApprovalEffect string

const (
	ApprovalEffectDeny ApprovalEffect = "deny"
	ApprovalEffectAsk  ApprovalEffect = "ask"
)

type ApprovalCategory string

const (
	ApprovalCategoryTool    ApprovalCategory = "tool"
	ApprovalCategoryCommand ApprovalCategory = "command"
	ApprovalCategoryMCP     ApprovalCategory = "mcp"
	ApprovalCategoryPath    ApprovalCategory = "path"
	// ApprovalCategoryHost gates outbound network by hostname — the network parallel to
	// `path`. Pattern is a host glob; the in-app egress proxy consults these rules.
	ApprovalCategoryHost ApprovalCategory = "host"
	// ApprovalCategoryWriteRoot is a bounded write-root approval lease.
	ApprovalCategoryWriteRoot    ApprovalCategory = "write_root"
	ApprovalCategoryHostResource ApprovalCategory = "host_resource"
)

type ApprovalRuleScope string

const (
	ApprovalRuleScopeDevice  ApprovalRuleScope = "device"
	ApprovalRuleScopeProject ApprovalRuleScope = "project"
)

type ApprovalGrantScope string

const (
	ApprovalGrantScopeChat    ApprovalGrantScope = "chat"
	ApprovalGrantScopeProject ApprovalGrantScope = "project"
	ApprovalGrantScopeDevice  ApprovalGrantScope = "device"
)

type ApprovalGrantCategory string

const (
	ApprovalGrantCategoryTool         ApprovalGrantCategory = "tool"
	ApprovalGrantCategoryMCP          ApprovalGrantCategory = "mcp"
	ApprovalGrantCategoryPath         ApprovalGrantCategory = "path"
	ApprovalGrantCategoryHost         ApprovalGrantCategory = "host"
	ApprovalGrantCategoryWriteRoot    ApprovalGrantCategory = "write_root"
	ApprovalGrantCategoryActionSet    ApprovalGrantCategory = "action_set"
	ApprovalGrantCategorySocketPath   ApprovalGrantCategory = "socket_path"
	ApprovalGrantCategoryHostResource ApprovalGrantCategory = "host_resource"
	// ApprovalGrantCategorySecret releases exact host-only fingerprints.
	ApprovalGrantCategorySecret ApprovalGrantCategory = "secret"

	// ApprovalGrantCategorySecretRedact strips covered credentials before sending.
	ApprovalGrantCategorySecretRedact ApprovalGrantCategory = "secret_redact"
	// Direct-IP grants cover an exact action and last for the chat.
	ApprovalGrantCategoryDirectIP ApprovalGrantCategory = "direct_ip"
	// Local-listener grants last for the chat and preserve egress mode.
	ApprovalGrantCategoryLocalListen ApprovalGrantCategory = "local_listen"
	// Loopback grants authorize chat access to services on this machine.
	ApprovalGrantCategoryLoopbackConnect ApprovalGrantCategory = "loopback_connect"
	// Command-network grants cover the exact command while retaining mediated observation.
	ApprovalGrantCategoryEgressCommand ApprovalGrantCategory = "egress_command"
	// Execution-capability grants cover process control or host execution until the chat is deleted.
	ApprovalGrantCategoryExecutionCapability ApprovalGrantCategory = "execution_capability"
	// Agent-policy grants cover changes to trust-surface files until the chat is deleted.
	ApprovalGrantCategoryAgentPolicy ApprovalGrantCategory = "agent_policy"
	// Package-coordinate grants authorize execution of verified remote package coordinates.
	ApprovalGrantCategoryPackageCoordinate ApprovalGrantCategory = "package_coordinate"
)

// SocketGrantAuthorityWarning is the fixed Settings/card authority copy for AF_UNIX leases.
const SocketGrantAuthorityWarning = "This service runs outside the sandbox with your permissions and may act on files, processes, devices, or the network."

// SocketGrantRevokeAppliesTo is the fixed revoke consequence copy for socket leases.
const SocketGrantRevokeAppliesTo = "Applies to the next command or terminal. Already-running processes retain access."

// --- Settings: Security scanners ---

type LandedChangeScope string

const (
	LandedChangeScopePathScoped LandedChangeScope = "path_scoped"
	LandedChangeScopeFullRoot   LandedChangeScope = "full_root"
)

func IsKnownLandedChangeScope(s LandedChangeScope) bool {
	switch s {
	case LandedChangeScopePathScoped, LandedChangeScopeFullRoot:
		return true
	default:
		return false
	}
}

// SourceVerify is how much of the tree a scan's source publication re-reads.
type SourceVerify string

const (
	SourceVerifyStat    SourceVerify = "stat"
	SourceVerifyContent SourceVerify = "content"
)

func IsKnownSourceVerify(s SourceVerify) bool {
	switch s {
	case SourceVerifyStat, SourceVerifyContent:
		return true
	default:
		return false
	}
}

type PricingSourceStatus string

const (
	PricingSourceStatusOK      PricingSourceStatus = "ok"
	PricingSourceStatusStale   PricingSourceStatus = "stale"
	PricingSourceStatusError   PricingSourceStatus = "error"
	PricingSourceStatusOffline PricingSourceStatus = "offline"
)

// TrustSurfaceId identifies one surface of project-supplied configuration.
type TrustSurfaceId string

const (
	TrustSurfaceAgentsMD             TrustSurfaceId = "agents_md"
	TrustSurfaceSkills               TrustSurfaceId = "skills"
	TrustSurfaceProjectSettings      TrustSurfaceId = "project_settings"
	TrustSurfaceProjectMCP           TrustSurfaceId = "project_mcp"
	TrustSurfaceScanConfig           TrustSurfaceId = "scan_config"
	TrustSurfacePromptOverrides      TrustSurfaceId = "prompt_overrides"
	TrustSurfaceExtensionConfig      TrustSurfaceId = "extension_config"
	TrustSurfaceExtensionSuggestions TrustSurfaceId = "extension_suggestions"
)

// TrustSurfaceGroup places a surface in the Trust panel.
type TrustSurfaceGroup string

const (
	// TrustGroupSteering shapes how the agent works.
	TrustGroupSteering TrustSurfaceGroup = "steering"
	// TrustGroupReplacesDefaults substitutes the app's own behaviour.
	TrustGroupReplacesDefaults TrustSurfaceGroup = "replaces_defaults"
	// TrustGroupSuggestion proposes device installs; nothing applies from it.
	TrustGroupSuggestion TrustSurfaceGroup = "suggestion"
)

// --- Verify settings ---

// VerifySuggestionState is the derived per-project state of the "set a test command"
// suggestion. The host makes this decision; the client renders a nudge only on "suggest".
type VerifySuggestionState string

const (
	VerifySuggestionStateAccepted  VerifySuggestionState = "accepted"  // a command is declared
	VerifySuggestionStateSuggest   VerifySuggestionState = "suggest"   // detected, undeclared, not dismissed → nudge
	VerifySuggestionStateWaiting   VerifySuggestionState = "waiting"   // detection ran, found nothing
	VerifySuggestionStateDismissed VerifySuggestionState = "dismissed" // user dismissed the suggestion (sticky)
	VerifySuggestionStateUnknown   VerifySuggestionState = "unknown"   // detection has not run yet
)

// --- Detection packs ---

// DetectionPackRejectedRule names a rule file that failed validation on import.
type DetectionPackRejectedRule struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}
