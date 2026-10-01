package oar

import (
	"github.com/lycaon/lycaon/internal/toolcontract"
	"strings"
	"sync"
)

// ContentSegment is one host-attributed segment at a content anchor. These
// fields come from the structured occurrence envelope.
type ContentSegment struct {
	Content   string
	Role      string
	Origin    string
	Authority string
	TrustTier string
	Source    string
}

// GuardContext is the occurrence's typed fact set.
type GuardContext struct {
	// FeedbackData supplies value-free occurrence metadata for decision presentation.
	FeedbackData map[string]any

	mu sync.Mutex

	// Cheap turn / session facts.
	Tool           string
	ToolArgs       map[string]any
	LastAssistant  string
	TurnTools      []string
	ProjectID      string
	SessionID      string
	SessionPosture string
	Surface        string
	Profile        string
	Phase          string
	// WorkerLeg reports a delegated leg.
	WorkerLeg bool

	// Derived roster / scope / posture observations.
	WorkersIdle                  bool
	WorkerSpawnBlocked           bool
	ActiveWorkerCount            int64
	PendingOverlayPromote        bool
	OverlayState                 string
	SurfaceMayFinish             bool
	IsHostCycleTurn              bool
	BatchPhase                   string
	BatchClosed                  bool
	ProgressOpenItems            int64
	ProgressHasOpenSteps         bool
	HasCompletionReport          bool
	TaskEnvelopeEcho             bool
	CloseoutSurface              bool
	ProgressReconcileNeeded      bool
	VerifyRequired               bool
	VerifierPass                 bool
	SynthesisWrapupToolForbidden bool
	ProgressClosureArmed         bool
	ProgressGatedTool            bool
	ProgressMissing              bool
	ReviewLoopActive             bool
	PathIsWorkerBranch           bool
	VerifyHasCommand             bool
	VerifyDeclared               bool
	ProgressClosedBeyondBaseline bool
	ProgressReconciledSinceArm   bool
	PendingUserInput             bool
	StubValid                    bool
	ToolAllowedForProfile        bool
	HabitRedirectMatch           string
	WriteRoots                   []string
	PathOutsideScope             bool // precomputed for the active tool call
	PatternParseOK               bool
	ArgValidationErrors          []string
	ArgValidationReason          string
	ArgValidationField           string
	// ActionHostResources and environment maps are host-resolved machine state
	// for host-resource policy. ActionHostResourceDenials is the external
	// authorization layer's exact deny outcome, not an OAR rule verdict.
	ActionHostResources       []string
	ActionHostResourceDenials []string
	// Confine observations describe one command, finished or running.
	ConfineApplied bool
	NetworkMode    string
	DenialSubject  string
	ConfineSignals []string
	// FailedStages are the command lines of the finished invocation's failed
	// stages.
	FailedStages   []string
	ProcessRunning bool
	// SandboxRefusals quotes each operation the kernel reported refusing.
	SandboxRefusals []string
	// Refused paths are paths the kernel refused; the grant lists name what
	// each layer's recovery would request.
	RefusedWritePaths  []string
	RefusedWriteGrants []string
	RefusedReadPaths   []string
	RefusedReadGrants  []string
	// The remaining refusals, by the capability that would admit them.
	RefusedSocketPaths  []string
	RefusedConnectPorts []string
	RefusedListenPorts  []string
	RefusedSignals      []string
	UnsandboxedRefusals []string
	// Worktree paths are agent-policy files a command's Git index change left
	// behind: stale content, files the index dropped, and unmerged stages.
	WorktreeStalePaths    []string
	WorktreeLeftoverPaths []string
	WorktreeConflictPaths []string
	HostResourceStatus    map[string]string
	HostResourcePolicy    map[string]string
	ModeBits              string
	ToolArgsFingerprint   string

	// Scaffold and posture observations.
	PostureUnresolved bool
	HighRiskTool      bool
	ToolIsState       bool
	ToolIsDelegation  bool
	ToolIsTask        bool
	// ToolPayloadChunkable is true when the tool's arguments carry a free-form
	// payload the model can split across calls, from the native-tool catalog.
	ToolPayloadChunkable bool
	ToolIsHandoff        bool
	PackRunnerTask       bool
	AgentIsPlanWriter    bool
	DisallowedAgent      bool
	PlanAwaitingApproval bool

	// Expensive evidence / ledger (lazy).
	UnobservedCitedPaths   []string
	UnobservedCitedURLs    []string
	UnobservedCitedHandles []string
	CitationFieldsPresent  bool
	ClaimsCompletion       bool
	HasMatchingLedgerJob   bool
	LedgerCriteriaMet      bool
	WorkerSummaryPresent   bool
	WorkerArtifactPresent  bool
	WorkerArtifactMeasured bool
	FilesTouched           []string
	SummaryLength          int64

	// Core tier ([OAR-FACT-15]). Anchor is a property of the occurrence, so
	// every rule evaluated at one observes the same value.
	Anchor    string
	FireCount int64

	// Profile members this host publishes to claim the tool, session and
	// content-safety profiles whole ([OAR-FACT-16]).
	PermissionProfile        string
	Principal                string
	PrincipalRoles           []string
	ContentLength            int64
	ContentRoles             []string
	ContentOrigins           []string
	ContentAuthorities       []string
	ContentTrustTiers        []string
	ContentSources           []string
	ContentSegmentCount      int64
	ContentContainsUntrusted bool
	// Content is the detector-facing joined projection of ContentSegments at a
	// content-safety anchor (content.input / content.output / content.tool_result).
	Content    string
	ContentSet bool

	// Engine counters (filled from CounterStore).
	RepeatCount  int64
	BreakerCount int64
	// FruitlessSearchRun counts consecutive searches for one question that
	// returned no material, across reworded scope args.
	FruitlessSearchRun int64
	SameCodeRejectRun  int64
	// CodeRejectResponses counts model responses rejecting one tool with one
	// structured code, capped at the escalation threshold.
	CodeRejectResponses int64
	// DeferredUnactivated counts tools this surface defers that the session has
	// not activated. Non-zero means a repeat may be reaching for a name outside
	// the turn schema, which retrying cannot surface.
	DeferredUnactivated int64

	// Worker, delay, scope, and grounding observations.
	// WorkerAttemptedMutation is true when an implementer child invoked
	// write, edit, or replace_lines at least once.
	WorkerAttemptedMutation    bool
	BatchReadyIgnoringProgress bool
	SynthesisDelayCount        int64
	// ReviewVerdictGateOpen is true when the active review_loop phase's
	// evidence_passed gate is unsatisfied at closeout time (no terminal
	// submit_verdict recorded).
	ReviewVerdictGateOpen bool
	VerdictDelayCount     int64
	// CloseoutGatesOpen is true when the active phase declares
	// controls.closeout: gated and its completion gates are unsatisfied at
	// closeout time with no coordinator→human wait pending.
	CloseoutGatesOpen bool
	// CloseoutGateOpenLeaves formats the open gate leaves for policy copy.
	CloseoutGateOpenLeaves     string
	CloseoutGateDelayCount     int64
	WorkflowReportPhasePending bool
	PhaseObligationPending     bool
	// PhaseObligationKinds formats pending kinds for policy copy.
	PhaseObligationKinds       string
	ScopeMode                  string
	ProfileMutationCapable     bool
	BaseOverlayID              string
	BaseOverlayResolves        bool
	BaseOverlayChecked         bool
	BaseOverlayPending         bool
	ActiveReadCount            int64
	ActiveWriteCount           int64
	MaxWorkers                 int64
	MaxReadWorkers             int64
	MaxWriteWorkers            int64
	CitationUnverifiable       bool
	ScoutSurveyEvidencePresent bool
	SurfaceClaimUngrounded     bool
	PageMeasureUngrounded      bool
	AgentIsScout               bool
	AgentIsImplementer         bool
	ProfileSurveysProjectTree  bool
	// ProfileFetchesURLs is true when this agent's profile can retrieve a URL,
	// so guidance may tell it to observe one before citing it.
	ProfileFetchesURLs  bool
	RepoKnownEmpty      bool
	LastAuditUngrounded bool
	GroundingEscalated  bool

	CommandNotArgv bool

	// Shared tool-plane observations select rule decisions.
	IsDirectory       bool
	NotFound          bool
	PathDenied        bool
	BulkDenied        bool
	BinaryDenied      bool
	ModeDenied        bool
	PathEscape        bool
	BeyondEOF         bool
	NotRunning        bool
	Unsupported       bool
	ResourceLimit     bool
	Conflict          bool
	PathRequired      bool
	IDRequired        bool
	PolicyDenied      bool
	UnknownTarget     bool
	Missing           bool
	Forbidden         bool
	SelectorEmpty     bool
	SelectorAmbiguous bool
	RejectObservation string // snake_case one-off / collision token (≠ hint id)

	// Detector class — observation facts from Detector.Inspect.
	PromptInjectionScore float64
	JailbreakScore       float64
	PIIEntities          []any
	SecretMatches        []any

	// PathOutsideScopeByTool backs path_outside_scope(tool) observations.
	PathOutsideScopeByTool map[string]bool
	// SourceIncludes backs source_includes(id).
	SourceIncludes map[string]bool

	// RecentToolNames is the tool-name sequence that flow patterns match.
	RecentToolNames []string

	// MCP structural observations. Zero for non-mcp_* tools.
	MCPProviderID         string
	MCPToolName           string
	MCPQualifiedTool      string
	MCPProviderConfigured bool
	MCPProviderEnabled    bool
	MCPCallOK             bool
	MCPErrorCode          string
	MCPSchemaMatched      bool
	MCPFields             map[string]any
	// MCPResultText is the MCP CallTool result body for lazy schema binding.
	MCPResultText string
	// MCPCatalog backs parameterized mcp_provider_configured_for/enabled_for(id) during eval.
	MCPCatalog MCPCatalogView

	// EditorConfigMismatch is true when the invocation landed text that breaks
	// rules its .editorconfig chain declares.
	EditorConfigMismatch      bool
	SourceAnalysisUnavailable bool
	SyntaxCheckOverridden     bool
	// HTTPRequestWebPage is true when http_request delivered a remote HTML
	// document that fetch_url could have read this turn.
	HTTPRequestWebPage bool

	// RejectData maps hint code → template vars for the Decision renderer.
	// Populated by schema/policy observation providers (never a verdict).
	RejectData map[string]map[string]any
	// ObservationData is the occurrence-wide host metadata shared by every rule consumer.
	ObservationData map[string]any
	// ObservedRejectCode publishes the intrinsic failure identity; it does not select rules.
	ObservedRejectCode string

	// Published facts override typed zero values during activation.
	Published map[string]any

	// Lazy providers run outside mu so they can use observation setters.
	providers         map[string]FactProvider
	computed          map[string]bool
	providerRunning   map[string]chan struct{}
	providerErrors    map[string]error
	mcpSchemaComputed bool
	mcpSchemaError    error
}

// ClearMCPObservation zeros all MCP structural facts and clears MCPFields.
func (gc *GuardContext) ClearMCPObservation() {
	if gc == nil {
		return
	}
	gc.MCPProviderID = ""
	gc.MCPToolName = ""
	gc.MCPQualifiedTool = ""
	gc.MCPProviderConfigured = false
	gc.MCPProviderEnabled = false
	gc.MCPCallOK = false
	gc.MCPErrorCode = ""
	gc.MCPSchemaMatched = false
	gc.MCPFields = nil
	gc.MCPResultText = ""
	gc.MCPCatalog = nil
}

// FactProvider computes one named fact into gc. Observation only.
type FactProvider func(gc *GuardContext) error

// NewGuardContext returns an empty fact set.
func NewGuardContext() *GuardContext {
	return &GuardContext{
		ToolArgs:               map[string]any{},
		providers:              map[string]FactProvider{},
		computed:               map[string]bool{},
		providerErrors:         map[string]error{},
		PathOutsideScopeByTool: map[string]bool{},
		SourceIncludes:         map[string]bool{},
		RejectData:             map[string]map[string]any{},
	}
}

// RegisterProvider registers a lazy fact provider by catalogue name.
func (gc *GuardContext) RegisterProvider(name string, p FactProvider) {
	if gc == nil || name == "" || p == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if gc.providers == nil {
		gc.providers = map[string]FactProvider{}
	}
	gc.providers[name] = p
}

// Ensure runs the named provider once if registered.
func (gc *GuardContext) Ensure(name string) error {
	if gc == nil || name == "" {
		return nil
	}
	gc.mu.Lock()
	if gc.computed[name] {
		err := gc.providerErrors[name]
		gc.mu.Unlock()
		return err
	}
	if pending := gc.providerRunning[name]; pending != nil {
		gc.mu.Unlock()
		<-pending
		gc.mu.Lock()
		err := gc.providerErrors[name]
		gc.mu.Unlock()
		return err
	}
	if gc.providerRunning == nil {
		gc.providerRunning = map[string]chan struct{}{}
	}
	pending := make(chan struct{})
	gc.providerRunning[name] = pending
	provider := gc.providers[name]
	gc.mu.Unlock()
	var err error
	if provider != nil {
		err = provider(gc)
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	if gc.computed == nil {
		gc.computed = map[string]bool{}
	}
	if gc.providerErrors == nil {
		gc.providerErrors = map[string]error{}
	}
	gc.computed[name] = true
	gc.providerErrors[name] = err
	delete(gc.providerRunning, name)
	close(pending)
	return err
}

// SetCodeRejectResponses records the bounded rejected-response count as the
// code_reject_responses fact.
func (gc *GuardContext) SetCodeRejectResponses(n int64) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.CodeRejectResponses = n
}

// SetFruitlessSearchRun records the host-observed consecutive-fruitless-search
// run as the fruitless_search_run fact.
func (gc *GuardContext) SetFruitlessSearchRun(n int64) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.FruitlessSearchRun = n
}

// SetRepeatCount records an authoritative host repeat count.
func (gc *GuardContext) SetRepeatCount(n int64) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.RepeatCount = n
}

// SetDeferredUnactivated records how many of this surface's deferred tools the
// session has not yet activated.
func (gc *GuardContext) SetDeferredUnactivated(n int64) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.DeferredUnactivated = n
}

// SetContentSegments records the structured content occurrence and refreshes
// the aligned content-provenance profile plus the detector-facing text buffer.
func (gc *GuardContext) SetContentSegments(segments []ContentSegment) {
	if gc == nil {
		return
	}
	gc.mu.Lock()
	defer gc.mu.Unlock()
	gc.ContentRoles = make([]string, 0, len(segments))
	gc.ContentOrigins = make([]string, 0, len(segments))
	gc.ContentAuthorities = make([]string, 0, len(segments))
	gc.ContentTrustTiers = make([]string, 0, len(segments))
	gc.ContentSources = make([]string, 0, len(segments))
	contents := make([]string, 0, len(segments))
	gc.ContentContainsUntrusted = false
	for _, segment := range segments {
		contents = append(contents, segment.Content)
		gc.ContentRoles = append(gc.ContentRoles, provenanceValue(segment.Role))
		gc.ContentOrigins = append(gc.ContentOrigins, provenanceValue(segment.Origin))
		gc.ContentAuthorities = append(gc.ContentAuthorities, provenanceValue(segment.Authority))
		trust := provenanceValue(segment.TrustTier)
		gc.ContentTrustTiers = append(gc.ContentTrustTiers, trust)
		gc.ContentSources = append(gc.ContentSources, strings.TrimSpace(segment.Source))
		if trust == "untrusted" {
			gc.ContentContainsUntrusted = true
		}
	}
	gc.ContentSegmentCount = int64(len(segments))
	gc.Content = strings.Join(contents, "\n")
	gc.ContentSet = true
	gc.ContentLength = int64(len([]rune(gc.Content)))
}

func provenanceValue(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return "unknown"
}

// DeriveToolClassFacts fills tool_is_* / high_risk observations from Tool.
func (gc *GuardContext) DeriveToolClassFacts() {
	if gc == nil {
		return
	}
	t := gc.Tool
	gc.ToolIsState = hasPrefix(t, "state_")
	gc.ToolIsDelegation = hasPrefix(t, "delegate_")
	gc.ToolIsHandoff = hasPrefix(t, "handoff_")
	gc.ToolIsTask = t == "task"
	gc.PackRunnerTask = t == "task" // observation: task tool used; agent-type filter is separate
	if contract, ok := toolcontract.Lookup(t); ok {
		gc.ToolPayloadChunkable = contract.ChunkablePayload
	}
	gc.HighRiskTool = gc.ToolIsState || gc.ToolIsDelegation || gc.ToolIsHandoff || gc.ToolIsTask
	gc.PostureUnresolved = gc.SessionPosture == "" || gc.SessionPosture == "unresolved"
	if gc.PermissionProfile == "" {
		gc.PermissionProfile = gc.Profile
	}
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}
