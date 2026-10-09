// Package tools defines tool registration and execution.
package tools

import (
	"github.com/lycaon/lycaon/internal/hostprocess"

	"context"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/packageexec"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

// FileEditCapture is optional Den UI metadata for completed file mutation tools.
type FileEditCapture struct {
	Path   string
	Before *string
	After  string
}

// VisualCapture is optional visual artifact bytes for store-backed projection.
type VisualCapture struct {
	Mime     string
	Bytes    []byte
	Source   api.VisualArtifactSource
	Caption  string
	Perceive bool
	// Projected confirms capture projection completed.
	Projected bool
	// Width and Height declare a frame archive's viewport; a raster is measured from its bytes.
	Width, Height int
	// ArtifactID names a stored artifact whose exact bytes this result shows
	// again. The result references it and stores nothing new.
	ArtifactID string
}

// SourceRunCapture is a settled source run.
type SourceRunCapture struct {
	CheckID          string
	IsCheck          bool
	Command          string
	ExitCode         int
	Verdict          string
	SourceRevision   string
	SourceRootDigest string
	Cwd              string
}

// AgentNoteCapture is a grounded note produced by one invocation.
type AgentNoteCapture struct {
	SourceContext *api.SourceContext
	MessageID     string
	Content       string
	Grounding     *api.CitationGrounding
	// ArtifactIDs lists stills shown with the note.
	ArtifactIDs []string
}

// ToolInvocationOut collects host-side invocation outputs.
type ToolInvocationOut struct {
	// DisplaySubject names the resolved target before durable secret screening.
	DisplaySubject  string
	sourceLocations map[string]struct{}
	SourceContext   *api.SourceContext
	// OwnerInvoked is set at the handler boundary.
	OwnerInvoked bool
	// OwnerRef identifies a concrete resource of the selected subsystem.
	OwnerRef string
	// Facts carries the structured outcome.
	Facts guidance.ToolResultFacts
	// Dispatch is a producer-stated worker enqueue outcome.
	Dispatch *api.WorkerDispatch
	// Completion is a producer-stated lifecycle completion outcome.
	Completion       *api.ToolCompletion
	OverlayPromotion *api.OverlayPromotion
	FileEdit         *FileEditCapture
	Visual           *VisualCapture
	AgentNote        *AgentNoteCapture
	// ExternalAccess is Den UI metadata for exceptional external-access on this result.
	ExternalAccess *api.ExternalAccess
	// Skill is Den UI metadata for the skill a skills_read activation resolved to.
	Skill *api.SkillActivation
	// Process identifies a live process returned by the tool.
	Process *api.ToolProcessHandle
	// Verdict is Den UI metadata for a recorded review_loop verdict.
	Verdict *api.VerdictOutcome
	// SourceRun is nil until a command or verify run exits.
	SourceRun *SourceRunCapture
	// RetrievedFrom is the final retrieval host after redirects.
	RetrievedFrom string
	// SourceReads is the project text the handler returned, before session output limits.
	SourceReads []agentpresence.Read
}

// WorkerWriteCoordinator coordinates isolated worker writes.
type WorkerWriteCoordinator interface {
	BeforeWorkerWrite(ctx context.Context, tctx ToolContext, relPath string) error
	AfterWorkerWrite(ctx context.Context, tctx ToolContext, relPath string)
	ReleaseWorkerReservations(ctx context.Context, parentSessionID, jobID string) error
	// EnsureWorkerBranch prepares a write worker's private branch.
	EnsureWorkerBranch(ctx context.Context, tctx ToolContext) (ToolContext, error)
}

// PrimaryMutationRecorder records primary-tree paths for rewind checkpoints.
type PrimaryMutationRecorder interface {
	RecordPrimaryMutation(ctx context.Context, sessionID, relPath string)
}

// ContainerRecorder records invocation-launched container IDs for session provenance.
type ContainerRecorder interface {
	RecordSessionContainer(chatSessionID, containerID, socketPath string)
}

// ToolContext is per-invocation context for a tool handler.
type ToolContext struct {
	// Invocation is fixed before the handler runs.
	Invocation Invocation
	Identity   InvocationIdentity
	Source     InvocationSource
	Turn       InvocationTurn
	Files      InvocationFiles
	Host       InvocationHost
	Socket     InvocationSocket
	Direct     InvocationDirect
	Execution  InvocationExecution
	Local      InvocationLocal
	Effects    InvocationEffects
}

// InvocationIdentity carries call attribution.
type InvocationIdentity struct {
	ProjectID  string
	SessionID  string
	MessageID  string
	ToolCallID string
	Agent      string
	// UserTurn attributes writes to a user-turn ordinal.
	UserTurn         int
	WorkerJobID      string
	ParentSessionID  string
	RootSessionID    string
	HandoffSessionID string
	HandoffAgentID   string
}

// InvocationSource carries the source workspace and mutation services.
type InvocationSource struct {
	ProjectSourceBranch sourcebranch.ID
	ProjectRootBranches map[string]sourcebranch.ID
	// ModelSourceContext is the source context of the request that proposed this call.
	ModelSourceContext *api.SourceContext
	// EditorReadBases freezes reads known before this response executes tools;
	// the response's own accepted writes advance it.
	EditorReadBases     *AgentReadBases
	Roots               []projectroot.RootRef
	ActiveRootID        string
	SourceWorkspaceKind api.SourceWorkspaceKind
	WorkerBranchRoot    string
	// WorkerSourceRoots are primary roots captured into the private branch.
	WorkerSourceRoots []string
	// BranchWorkspace validates and prepares the branch.
	BranchWorkspace BranchWorkspace
	WorkerCoord     WorkerWriteCoordinator
	// MutationRecorder accrues writes for the next rewind checkpoint.
	MutationRecorder PrimaryMutationRecorder
	// SourceLedger records project mutations in this app instance.
	SourceLedger    sourceledger.Recorder
	SourceMutations sourceeffect.Journal
	// EditorDocuments serves and lands text for files the person has open.
	// Nil when the app runs without an editor host; tools then use the file.
	EditorDocuments EditorDocuments
	// RepoFileCount is the active root's observed file count.
	RepoFileCount int
	// RepoFileCountKnown distinguishes zero from unknown.
	RepoFileCountKnown bool
	// RepoTopLevel is the observed top-level layout.
	RepoTopLevel []string
}

// InvocationTurn carries the compiled model turn.
type InvocationTurn struct {
	ToolAccess    sandbox.ToolAccess
	TurnSurfaceID string
	// TurnOfferedToolNames binds execution to the schemas on this model request.
	// Nil leaves non-model callers unconstrained; empty permits no tools.
	TurnOfferedToolNames []string
	// TurnOfferedToolSchemas freezes argument shapes from the model request.
	TurnOfferedToolSchemas map[string]map[string]any
	// TurnToolPlan freezes availability for this model turn.
	// Zero indicates a caller without a compiled model surface.
	TurnToolPlan toolsurface.Plan
	// TurnWritePinRootID and Globs constrain writes to one root.
	TurnWritePinRootID string
	TurnWritePinGlobs  []string
}

// InvocationFiles carries reviewed file authority.
type InvocationFiles struct {
	// ApprovedFileAccess is exact path authority for this invocation only.
	ApprovedFileAccess []hitl.GrantedPathDelta
	// PreparedFileAccess permits staging; FileChangeReview authorizes the mutation.
	PreparedFileAccess []hitl.GrantedPathDelta
	// GrantedWriteRoots holds this invocation’s reviewed write roots.
	GrantedWriteRoots []string
	PolicyWriteGrants []confine.ProtectedPathGrant
	FileChangeReview  FileChangeReviewer
	contentReviews    *contentReviews
	// SessionReadPaths are exact protected-read grants.
	SessionReadPaths []string
	// ReadRoots are host-selected read-only confinement exceptions.
	ReadRoots []string
	// PackageExecution carries the host-resolved package action.
	PackageExecution *packageexec.Execution
}

// InvocationHost carries host realization and scratch paths.
type InvocationHost struct {
	// HostDataDir contains readable project-scoped host data.
	HostDataDir string
	// SessionScratchDir contains writable session-scoped scratch data.
	SessionScratchDir string
	// MaxToolSpillBytes shares the retention bound with recovery reads.
	MaxToolSpillBytes int
	// HostResources are exact catalog ids attached to the structured action.
	HostResources        []string
	HostResourceFamilies []string
	// HostResourceAsk is the subset carrying an additive organizational ask.
	HostResourceAsk []string
	// HostResourcePathExtra appends helper directories to the child PATH.
	HostResourcePathExtra []string
	// RealizationWriteRoots extend this spawn's confinement overlay.
	RealizationWriteRoots []string
	// RealizationSockets are AF_UNIX targets from host resources.
	RealizationSockets []string
}

// InvocationSocket carries socket permits.
type InvocationSocket struct {
	// SocketGrants are host-authorized AF_UNIX literals for this invocation (set by executor preflight).
	SocketGrants []confine.SocketGrant
	// SocketScopes and SocketGrantStates share indexes.
	SocketScopes      []string
	SocketGrantStates []string
	// DurableSocketGrants are project/device exact AF_UNIX overlays stamped at preflight.
	DurableSocketGrants []confine.SocketGrant
	// AuthorizedSocketDigests are digests already covered by chat overlay or current-call permit.
	AuthorizedSocketDigests []string
	// SocketActionDigest binds current-call socket permits for spawn consumption.
	SocketActionDigest string
	// SocketCapabilityRuntime is wired by the executor for spawn-time permit consumption.
	SocketCapabilityRuntime SocketCapabilityRuntime
	// SocketAuthorizationSource is human|never_ask when socket authority was freshly authorized.
	SocketAuthorizationSource string
}

// InvocationDirect carries direct network permits.
type InvocationDirect struct {
	// DirectIPRequested is true when command asked for one-action direct outbound IP.
	DirectIPRequested bool
	// DirectIPDeclared are optional declared destination strings (audit context only).
	DirectIPDeclared []string
	// DirectIPActionDigest binds the current-call direct-IP permit for spawn consumption.
	DirectIPActionDigest string
	// DirectIPRequestDigest is the canonical declared-destination digest for the permit.
	DirectIPRequestDigest string
	// DirectIPConfineDigest is the would-be confinement digest for the permit.
	DirectIPConfineDigest string
	// DirectIPAuthorized is true after a current-call permit covers this invocation.
	DirectIPAuthorized bool
	// DirectIPCapabilityRuntime is wired by the executor for spawn-time permit consumption.
	DirectIPCapabilityRuntime DirectIPCapabilityRuntime
	// DirectIPLifecycle optionally receives started/completed facts at the spawn boundary.
	DirectIPLifecycle DirectIPLifecycleHook
	// DirectIPBackground is true when the approved direct action is a background command job.
	DirectIPBackground bool
}

// InvocationExecution carries process authority.
type InvocationExecution struct {
	// VerificationCheck is host-resolved check intent for command execution.
	VerificationCheck bool
	ProcessReview     ProcessReviewer
	ProcessControl    bool
	HostExecution     bool
	executionPermit   *executionPermit
}

// InvocationLocal carries local network grants and audit.
type InvocationLocal struct {
	// ContainerRecorder records container launches for session loopback provenance.
	ContainerRecorder ContainerRecorder
	// SocksProxyEnv is true when the invocation asked for ALL_PROXY (mediated SOCKS).
	SocksProxyEnv bool
	// LocalListenGranted covers this invocation's local listener.
	LocalListenGranted bool
	// LocalListenPorts narrows the applied listener grant; empty means any local port.
	LocalListenPorts []uint16
	// LoopbackConnectGranted is true when chat authority covers outbound local connections.
	LoopbackConnectGranted bool
	// LoopbackConnectPorts narrows the local destination ports; empty means any.
	LoopbackConnectPorts []uint16
	// AuthzRecorder optionally records capability / mediated / direct lifecycle audit rows.
	AuthzRecorder authzledger.Recorder
}

// InvocationEffects carries invocation results and observations.
type InvocationEffects struct {
	// ReportProgress publishes host-measured work on this invocation's activity.
	ReportProgress func(api.ToolProgress)
	// ArgsTruncated marks arguments cut before parsing.
	ArgsTruncated bool
	// ArgsMalformed marks invalid JSON arguments.
	ArgsMalformed bool
	// CanonicalArgs preserve reference-bearing arguments for display.
	CanonicalArgs map[string]any
	// RequestedArgs are the arguments as sent, before the host expanded command
	// globs into CanonicalArgs; nil when nothing expanded.
	RequestedArgs map[string]any
	Secrets       *secretcap.Resolution
	// CredentialFiles receives credential-file reads and model-authored writes.
	CredentialFiles CredentialFiles
	Out             *ToolInvocationOut
	// Presence receives what this call does to project files as the handler
	// resolves it: targets, resolved mutations, approval holds, and landings.
	Presence PresenceReporter
}

// ChatSessionID returns the chat's root session, the identity chat-lifetime
// authority is keyed on, falling back to ParentSessionID, then SessionID.
func (tc ToolContext) ChatSessionID() string {
	if root := strings.TrimSpace(tc.Identity.RootSessionID); root != "" {
		return root
	}
	if parent := strings.TrimSpace(tc.Identity.ParentSessionID); parent != "" {
		return parent
	}
	return strings.TrimSpace(tc.Identity.SessionID)
}

// ProfileID is the calling agent's tool profile, defaulting an unspecified
// agent to the shared tool profile.
func (c ToolContext) ProfileID() string {
	if c.Identity.Agent != "" {
		return c.Identity.Agent
	}
	return toolprofiles.DefaultToolProfileID
}

// HoldForApproval marks a call's pending mutations as held by a checkpoint
// until the returned release runs.
func HoldForApproval(presence PresenceReporter, checkpointID string) func() {
	if presence == nil || checkpointID == "" {
		return func() {}
	}
	presence.AwaitingApproval(checkpointID)
	return presence.Approved
}

// PresenceReporter receives the structured file facts one tool call's handler holds.
type PresenceReporter interface {
	// Target names a project file the call works on.
	Target(target agentpresence.Target, kind api.AgentActivityKind)
	// Intents records resolved, unlanded mutations, replacing the call's earlier ones for the same targets.
	Intents(intents []agentpresence.Intent)
	// AwaitingApproval marks the call's intents as held by a checkpoint.
	AwaitingApproval(checkpointID string)
	// Approved releases a held call's intents.
	Approved()
	// Landed names the document revisions the call's mutations produced.
	Landed(documents []agentpresence.Document)
	// Reserved records paths a worker reserved before drafting them.
	Reserved(targets []agentpresence.Target)
	// Released drops a worker's reservations; no targets releases them all.
	Released(targets []agentpresence.Target)
}

// ActiveRootPath returns the on-disk path for the session's active workspace root.
func (c ToolContext) ActiveRootPath() string {
	if len(c.Source.Roots) == 0 {
		return ""
	}
	if r, err := projectroot.ActiveRoot(c.Source.Roots, c.Source.ActiveRootID); err == nil {
		return r.Path
	}
	return ""
}

// ToolHandler executes a registered tool.
type ToolHandler func(ctx context.Context, args map[string]any, tctx ToolContext) (string, error)

// ToolSourceMCP marks a tool published from an enabled MCP provider.
const ToolSourceMCP = "mcp"

// ToolMeta describes a tool for listing and JSON Schema args.
type ToolMeta struct {
	Name        string
	Description string
	Tags        []string
	ArgsSchema  map[string]any
	// ApprovalCategory and ApprovalSubject identify structured grants.
	ApprovalCategory string
	ApprovalSubject  string
	// UntrustedMetadata marks externally supplied descriptions and schemas.
	UntrustedMetadata bool
	// Deferred marks tools loaded on demand.
	Deferred bool
	// ReadOnlyHint carries the server's read-only annotation.
	ReadOnlyHint bool
	// Source is the registration origin. Empty for native catalog tools.
	Source string
	// SourceID is the origin instance (MCP provider id). Empty for native tools.
	SourceID string
	// AlwaysLoad keeps this dynamic schema on every eligible model call.
	AlwaysLoad bool
}

// IsMCP reports a tool published from an enabled MCP provider.
func (m ToolMeta) IsMCP() bool {
	if m.Source == ToolSourceMCP {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(m.Name), "mcp_")
}

// Invocation is the immutable action envelope passed to a subsystem owner.
type Invocation struct {
	ID             string
	ProjectID      string
	SessionID      string
	MessageID      string
	ToolCallID     string
	ToolName       string
	ArgsDigest     string
	ContractDigest string
	Contract       toolcontract.Contract
}

// Definition is one atomically published registry entry.
type Definition struct {
	Meta     ToolMeta
	Contract toolcontract.Contract
	Handler  ToolHandler
}

// ToolRegistry registers and runs tool definitions.
type ToolRegistry interface {
	Register(name string, handler ToolHandler) error
	RegisterDefinition(def Definition) error
	Definition(name string) (Definition, bool)
	Run(ctx context.Context, name string, args map[string]any, tctx ToolContext) (string, error)
	List() []ToolMeta
}

type ProcessReviewer func(context.Context, string, []hostprocess.Process) error

// SetDisplaySubject retains the readable target for durable screening and presentation.
func (c ToolContext) SetDisplaySubject(subject string) {
	if c.Effects.Out != nil {
		c.Effects.Out.DisplaySubject = subject
	}
}

// ExternalAccessBuildInput is the tools-layer input for stamping ToolResult.external_access.
// Built via SetExternalAccessBuilder to avoid an authzcontext import cycle.
type ExternalAccessBuildInput struct {
	Endpoints            []ExternalAccessEndpointFact
	Sockets              []ExternalAccessSocketFact
	DeclaredDestinations []string
	Direct               bool
	FullBypass           bool
}

// ExternalAccessEndpointFact is one mediated observation.
type ExternalAccessEndpointFact struct {
	Host      string
	Port      uint16
	Transport string
	Allowed   bool
	Attempts  int
}

// ExternalAccessSocketFact is one applied local-service socket.
type ExternalAccessSocketFact struct {
	ApprovedPath string
	ResolvedPath string
	Scope        string
}

var ExternalAccessBuilder func(ExternalAccessBuildInput) *api.ExternalAccess

// SetExternalAccessBuilder installs the authzcontext builder.
func SetExternalAccessBuilder(fn func(ExternalAccessBuildInput) *api.ExternalAccess) {
	ExternalAccessBuilder = fn
}

// CaptureExternalAccess builds ToolResult.external_access from applied machine
// facts on this invocation and stamps it on Out when present.
func CaptureExternalAccess(tctx ToolContext, hosts []confine.EgressHost, directApplied bool) {
	if tctx.Effects.Out == nil || ExternalAccessBuilder == nil {
		return
	}
	in := ExternalAccessBuildInput{
		Direct:     directApplied,
		FullBypass: confine.BypassEnabled() || confine.SandboxDisabled(),
	}
	if directApplied {
		in.DeclaredDestinations = append([]string(nil), tctx.Direct.DirectIPDeclared...)
	}
	for _, h := range hosts {
		in.Endpoints = append(in.Endpoints, ExternalAccessEndpointFact{
			Host:      h.Host,
			Port:      h.Port,
			Transport: h.Transport,
			Allowed:   h.Allowed,
			Attempts:  h.Attempts,
		})
	}
	for _, g := range tctx.Socket.SocketGrants {
		scope := "current_action"
		if tctx.Socket.SocketCapabilityRuntime != nil {
			for _, chat := range tctx.Socket.SocketCapabilityRuntime.AppliedGrants(tctx.ChatSessionID()) {
				if chat.ApprovedPath == g.ApprovedPath && chat.ResolvedPath == g.ResolvedPath {
					scope = "chat"
					break
				}
			}
		}
		in.Sockets = append(in.Sockets, ExternalAccessSocketFact{
			ApprovedPath: g.ApprovedPath,
			ResolvedPath: g.ResolvedPath,
			Scope:        scope,
		})
	}
	if len(in.Endpoints) == 0 && len(in.Sockets) == 0 && !in.Direct && !in.FullBypass {
		return
	}
	ea := ExternalAccessBuilder(in)
	if ea == nil {
		return
	}
	tctx.Effects.Out.ExternalAccess = ea
}

// ExternalAccessFromCapture returns a copy for ToolResult stamping.
func ExternalAccessFromCapture(ea *api.ExternalAccess) *api.ExternalAccess {
	if ea == nil {
		return nil
	}
	out := *ea
	if ea.Endpoints != nil {
		out.Endpoints = append([]api.ExternalAccessEndpoint(nil), ea.Endpoints...)
	}
	if ea.Sockets != nil {
		out.Sockets = append([]api.ExternalAccessSocket(nil), ea.Sockets...)
	}
	if ea.DeclaredDestinations != nil {
		out.DeclaredDestinations = append([]string(nil), ea.DeclaredDestinations...)
	}
	if ea.Detections != nil {
		out.Detections = append([]api.ExternalAccessDetection(nil), ea.Detections...)
	}
	if ea.Direct != nil {
		d := *ea.Direct
		out.Direct = &d
	}
	if ea.Modes != nil {
		out.Modes = append([]api.ExternalAccessMode(nil), ea.Modes...)
	}
	return &out
}
