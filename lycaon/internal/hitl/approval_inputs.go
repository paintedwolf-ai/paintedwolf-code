package hitl

import "github.com/lycaon/lycaon/internal/secretmatch"

const (
	SocketAuthorityOutsideSandboxDaemon = "outside_sandbox_daemon"
	DirectIPVisibilityUnobserved        = "unobserved"
	DirectIPAFUnixExactGrantsOnly       = "denied_except_exact_grants"
	DeclaredEndpointProviderCatalog     = "provider_catalog"
)

// ApprovalExplanation is host-authored copy explaining one approval subject.
type ApprovalExplanation struct {
	What      string
	Who       string
	IfWrong   string
	AllowLine string
}

// SocketCapabilityTarget is one canonical approved/resolved AF_UNIX pair.
type SocketCapabilityTarget struct {
	ApprovedPath       string
	ResolvedPath       string
	AlreadyChatGranted bool
}

// SocketCapability is the complete socket set reviewed atomically.
type SocketCapability struct {
	Targets            []SocketCapabilityTarget
	EffectiveAuthority string
}

// DirectIPCapability is host-derived direct-network context for one action.
type DirectIPCapability struct {
	Visibility           string
	AFUnix               string
	DeclaredDestinations []string
	ActionDigest         string
	CommandSummary       string
}

// DeclaredEndpoints is one host-declared destination set.
type DeclaredEndpoints struct {
	Hosts     []string
	HostCount int
	Digest    string
	Source    string
}

// SecretScreen is redaction-safe context for an outbound secret approval.
type SecretScreen struct {
	IgnoreCandidateID string
	Surface           string
	// SurfaceLabel is the display name for Surface.
	SurfaceLabel string
	CanRedact    bool
	CanTrack     bool
	// RedactionNote explains why redaction is unavailable.
	RedactionNote string
	// StandingRedactionHeld marks a redaction grant that cannot apply to this send.
	StandingRedactionHeld bool
	// Contested identifies a cited, single-use redaction receipt.
	Contested bool
	// VarName and Container carry harvest evidence for the citation.
	VarName          string
	Container        string
	DestinationID    string
	DestinationLabel string
	Recipients       []secretmatch.Recipient
	ConnectPorts     []uint16
	SecretNames      []string
	// DestinationKind distinguishes model providers, services, and local processes.
	DestinationKind secretmatch.DestinationKind
	// Managed marks exact managed-capability evidence.
	Managed bool
	// RedactionBreaks marks a seam where stripping the value fails the call.
	RedactionBreaks bool
	// ProviderID is the configured AI provider a model request is bound for;
	// empty on every other surface.
	ProviderID   string
	RuleID       string
	RuleTitle    string
	GenericShape string
	Occurrences  int
	SourceKind   string
	SourceTool   string
	SourcePath   string
	SourceLine   int
	// OriginKind is file or field. SourcePath is a project path or a field name.
	OriginKind secretmatch.OriginKind
	// SourceToolCallID is the originating tool row (for example a read).
	SourceToolCallID string
	ToolCallID       string
	// CommandLine is a host-redacted argv for command/terminal cards.
	CommandLine string
	// ScreeningGap names content the screen could not read.
	ScreeningGap secretmatch.ScreeningGap
	// Held names the person-held values this send would release.
	Held *HeldRelease
}
