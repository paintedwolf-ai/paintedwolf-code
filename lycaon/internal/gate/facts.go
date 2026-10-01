package gate

import (
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// Stage is the boundary where a decision is possible.
type Stage string

const (
	// StagePreSpawn is before a tool runs, with the boundary resolved.
	StagePreSpawn Stage = "pre_spawn"
	// StagePreDial is inside the mediated egress path, before CONNECT.
	StagePreDial Stage = "pre_dial"
	// StagePreSend is immediately before agent-authored content leaves.
	StagePreSend Stage = "pre_send"
)

// Producer is a bit set of fact reporters. Facts.Ran uses it to distinguish an
// empty report from a producer that did not run.
type Producer uint16

const (
	// ProducerContainment is the confine projection every action carries.
	ProducerContainment Producer = 1 << iota
	// ProducerDetection is the detection-pack engine.
	ProducerDetection
	// ProducerPayload is the outbound secret matcher.
	ProducerPayload
	// ProducerDestination is the mediated egress broker.
	ProducerDestination
	// ProducerFilePath reports the filesystem path an action names or was
	// refused on, together with its sensitive-locations classification.
	ProducerFilePath
	// ProducerConsent is the MCP tool-definition pin.
	ProducerConsent
	// ProducerLease is the grant runtime that reports existing authority.
	ProducerLease
	// ProducerRule is the merged user and project approval rule set.
	ProducerRule
	// ProducerExposure reports credential-class reads, inherited across spawns.
	ProducerExposure
	// ProducerIngestion reports that this chat has taken in external content the
	// host did not author, inherited across spawns.
	ProducerIngestion
	// ProducerApprovalRequest reports a host-structured capability ask or a
	// chat capability widening. The producer is the host, not the model field.
	ProducerApprovalRequest
	// ProducerPackageExecution reports package identity preflight.
	ProducerPackageExecution
)

// Has reports whether every producer in want reported.
func (p Producer) Has(want Producer) bool { return p&want == want }

// Containment projects the same confinement result applied by the executor.
type Containment struct {
	ProcessControl bool
	HostExecution  bool
	// SpawnsProcess distinguishes confined child processes from native host tools.
	SpawnsProcess bool
	// FSJailed is true when the write jail is active.
	FSJailed bool
	// Egress is "deny", "proxy", or "direct_ip"; empty when no jail applies.
	Egress string
	// DirectIP is true when the action runs with unmediated outbound networking.
	DirectIP bool
	// SocketCount is the number of AF_UNIX grants that will apply.
	SocketCount int
	// SocketPathsDigest identifies the socket set without naming paths.
	SocketPathsDigest string
}

// Egress modes on Containment — the locked vocabulary shared with the boundary.
const (
	EgressDeny     = "deny"
	EgressProxy    = "proxy"
	EgressDirectIP = "direct_ip"
)

// Match carries effect tags separately from detection severity. Untagged rules remain eligible for review.
type Match struct {
	PackID    string
	RuleID    string
	RuleTitle string
	Level     string
	// External and Local identify where the effect occurs; both can require review.
	External      bool
	Local         bool
	Unrecoverable bool
}

// SecretSource records the observed provenance of matched bytes.
type SecretSource string

const (
	// SecretSourceUnknown provenance requires disclosure review.
	SecretSourceUnknown SecretSource = "unknown"
	// SecretSourcePublicInbound identifies retrieval restricted to public destinations without ambient credentials.
	SecretSourcePublicInbound SecretSource = "public_inbound"
)

// SecretHit carries match metadata and provenance without the matched bytes.
type SecretHit struct {
	Surface     string
	RuleID      string
	RuleTitle   string
	Occurrences int
	Source      SecretSource
	SourceKind  string
	SourceTool  string
	// DestinationTrusted is a standing device decision bound to the resolved
	// transport identity; a repointed destination reports false.
	DestinationTrusted bool
	// ChatGenerated is true when every matched value is a managed secret the
	// host generated for this chat, so nothing outside the chat has held it.
	ChatGenerated bool
	// RecipientsLocal is true when every recipient is a process this chat runs,
	// a file a tool saves, or an HTTP service reached over loopback.
	RecipientsLocal bool
}

// Endpoint is one observed outbound destination.
type Endpoint struct {
	Host      string
	Port      uint16
	Transport string
	// ConfiguredBy names the configuration that supplied the destination.
	ConfiguredBy string
	// Configured identifies destinations supplied by user or project configuration.
	Configured bool
	// PublicRegistry names the catalogued public package registry serving the
	// host; empty for any other destination.
	PublicRegistry string
	// Opaque is true when the transport is a tunnel whose payload the broker
	// cannot read.
	Opaque bool
	// FirstUseThisSession is true when this session has not yet used this dial.
	FirstUseThisSession bool
}

// FileMode is which direction a filesystem action moves content.
type FileMode string

const (
	// ModeRead puts content from the path into the transcript.
	ModeRead FileMode = "read"
	// ModeWrite puts content from the agent onto the path.
	ModeWrite FileMode = "write"
)

// FileTarget is a filesystem path named by an action.
type FileTarget struct {
	Path string
	Mode FileMode
	// OutsideRoots is true when the path falls outside every attached root.
	OutsideRoots bool
	// WithinConfinement is OS scratch/cache or write-root authority the posture
	// accepts standing; Strict withholds it from writes.
	WithinConfinement bool
	// Sensitive reports a catalog match; attached roots suppress its gate.
	Sensitive bool
	// ProtectedSubject asks even inside attached roots.
	ProtectedSubject bool
	// CatalogID and CatalogTitle name the entry that matched, for citation.
	CatalogID    string
	CatalogTitle string
	// OtherTargetCount is how many further crossings a batch action names beyond
	// this worst one. Card disclosure only; the verdict ignores it.
	OtherTargetCount int
}

// Consent is the MCP definition pin for the tool being called.
type Consent struct {
	Tool string
	// DefinitionChanged is true when the server now serves a definition differing
	// from the one pinned when the user last allowed this tool to run.
	DefinitionChanged bool
}

// MCPCall describes an MCP tool invocation for the Strict confirmation.
type MCPCall struct {
	Tool string
}

// UserRule carries an ask rule; deny rules are enforced before gate evaluation.
type UserRule struct {
	Category string
	Pattern  string
	// Subject is what the pattern matched on this action, for citation.
	Subject string
}

// ApprovalRequest describes a validated, finite set of future actions.
type ApprovalRequest struct {
	Count int
}

// Host-defined capability axes.
const (
	AxisLocalListen     = "local_listen"
	AxisLoopbackConnect = "loopback_connect"
)

// CapabilityWidening lists local-network authority missing from the chat.
type CapabilityWidening struct {
	Axes  []string
	Ports []uint16
	// SelfSpawned reports that every requested connect port is served by this session.
	SelfSpawned bool
}

// PackageExecution is the redaction-safe registry identity for a package action.
type PackageExecution struct {
	Manager   string
	Operation string
	Packages  []PackageIdentity
}

// PackageIdentity is one canonical package coordinate and its preflight state.
type PackageIdentity struct {
	System              string
	Name                string
	Version             string
	AgeDays             *int
	Status              string
	SourceRepository    string
	VerifiedAttestation bool
}

// Facts contains reports from completed boundary checks.
type Facts struct {
	ProcessAccess             string
	ExecutionCapabilityLeased bool
	// AgentPolicyPaths are resolved agent-policy files a prepared write changes.
	AgentPolicyPaths []string
	// AgentPolicyLeased records chat authority covering every agent-policy path.
	AgentPolicyLeased bool
	Stage             Stage
	// Ran is the set of producers that reported for this action.
	Ran Producer

	Containment Containment
	// Recoverable records whether the host can undo the effect using its available snapshot.
	Recoverable bool
	// Leased records existing authority for this action and boundary. Each gate decides whether it permits reuse.
	Leased bool

	// RequestConsented covers connection prompts for an approved HTTP handoff; authored rules still apply.
	RequestConsented bool
	// LeasedExact records exact-action coverage, which authority_misuse requires for reuse.
	LeasedExact bool
	// LeasedPackage records package coordinate coverage.
	LeasedPackage bool
	// FileLeased records granted-path coverage for every filesystem boundary crossing.
	FileLeased bool

	Detection       *Match
	Payload         *SecretHit
	Destination     *Endpoint
	File            *FileTarget
	Consent         *Consent
	MCP             *MCPCall
	UserRule        *UserRule
	ApprovalRequest *ApprovalRequest
	// CapabilityWidening is set when this invocation would install
	// local-network authority the chat does not hold.
	CapabilityWidening *CapabilityWidening
	PackageExecution   *PackageExecution
	// SecretExposed is true when this chat has read credential-class content — the
	// value is in the context window, so it can leave in a form no pattern matches.
	SecretExposed bool
	// UntrustedIngested records external content already delivered to the chat.
	UntrustedIngested bool
}

// Fact identifies evidence supporting an approval reason.
type Fact struct {
	// Evaluate stamps Gate after ordering the approval reasons.
	Gate api.ApprovalGate
	// Key names the input, stable enough for tests and explain copy to key on.
	Key string
	// Value is the redaction-safe rendering shown to the person.
	Value string
	// Source names the layer that produced it.
	Source string
}

func fact(key, value, source string) Fact {
	return Fact{Key: key, Value: strings.TrimSpace(value), Source: source}
}
