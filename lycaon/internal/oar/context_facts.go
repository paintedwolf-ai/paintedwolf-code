package oar

import "sync"

type InvocationFacts struct {
	Tool                  string
	ToolArgs              map[string]any
	ToolArgsFingerprint   string
	ToolAllowedForProfile bool
	HabitRedirectMatch    string
	PatternParseOK        bool
	ArgValidationErrors   []string
	ArgValidationReason   string
	ArgValidationField    string
	ToolIsState           bool
	ToolIsDelegation      bool
	ToolIsTask            bool
	ToolPayloadChunkable  bool
	ToolIsHandoff         bool
	PackRunnerTask        bool
	CommandNotArgv        bool
	HTTPRequestWebPage    bool
}

type SessionFacts struct {
	LastAssistant     string
	TurnTools         []string
	ProjectID         string
	SessionID         string
	SessionPosture    string
	Surface           string
	Profile           string
	Phase             string
	WorkerLeg         bool
	SurfaceMayFinish  bool
	IsHostCycleTurn   bool
	CloseoutSurface   bool
	PendingUserInput  bool
	StubValid         bool
	PermissionProfile string
	Principal         string
	PrincipalRoles    []string
	PostureUnresolved bool
	RecentToolNames   []string
}

type ProgressFacts struct {
	ProgressOpenItems            int64
	ProgressHasOpenSteps         bool
	ProgressReconcileNeeded      bool
	ProgressClosureArmed         bool
	ProgressGatedTool            bool
	ProgressMissing              bool
	ProgressClosedBeyondBaseline bool
	ProgressReconciledSinceArm   bool
	HasCompletionReport          bool
	TaskEnvelopeEcho             bool
	VerifyRequired               bool
	VerifierPass                 bool
	VerifyHasCommand             bool
	VerifyDeclared               bool
}

type WorkflowFacts struct {
	BatchPhase                   string
	BatchClosed                  bool
	SynthesisWrapupToolForbidden bool
	ReviewLoopActive             bool
	PlanAwaitingApproval         bool
	BatchReadyIgnoringProgress   bool
	SynthesisDelayCount          int64
	ReviewVerdictGateOpen        bool
	VerdictDelayCount            int64
	CloseoutGatesOpen            bool
	CloseoutGateOpenLeaves       string
	CloseoutGateDelayCount       int64
	WorkflowReportPhasePending   bool
	PhaseObligationPending       bool
	PhaseObligationKinds         string
}

type WorkersFacts struct {
	WorkersIdle             bool
	WorkerSpawnBlocked      bool
	ActiveWorkerCount       int64
	PendingOverlayPromote   bool
	OverlayState            string
	PathIsWorkerBranch      bool
	WorkerAttemptedMutation bool
	ScopeMode               string
	ProfileMutationCapable  bool
	BaseOverlayID           string
	BaseOverlayResolves     bool
	BaseOverlayChecked      bool
	BaseOverlayPending      bool
	ActiveReadCount         int64
	ActiveWriteCount        int64
	MaxWorkers              int64
	MaxReadWorkers          int64
	MaxWriteWorkers         int64
	AgentIsScout            bool
	AgentIsImplementer      bool
	AgentIsPlanWriter       bool
	DisallowedAgent         bool
}

type GroundingFacts struct {
	UnobservedCitedPaths       []string
	UnobservedCitedURLs        []string
	UnobservedCitedHandles     []string
	CitationFieldsPresent      bool
	ClaimsCompletion           bool
	HasMatchingLedgerJob       bool
	LedgerCriteriaMet          bool
	WorkerSummaryPresent       bool
	WorkerArtifactPresent      bool
	WorkerArtifactMeasured     bool
	FilesTouched               []string
	SummaryLength              int64
	CitationUnverifiable       bool
	ScoutSurveyEvidencePresent bool
	SurfaceClaimUngrounded     bool
	PageMeasureUngrounded      bool
	ProfileSurveysProjectTree  bool
	ProfileFetchesURLs         bool
	RepoKnownEmpty             bool
	LastAuditUngrounded        bool
	GroundingEscalated         bool
}

type ExecutionFacts struct {
	ConfineApplied  bool
	NetworkMode     string
	DenialSubject   string
	ConfineSignals  []string
	FailedStages    []string
	ProcessRunning  bool
	SandboxRefusals []string
	ModeBits        string
}

type RefusalsFacts struct {
	RefusedWritePaths         []string
	RefusedWriteGrants        []string
	RefusedReadPaths          []string
	RefusedReadGrants         []string
	RefusedTerminalReadPaths  []string
	RefusedTerminalWritePaths []string
	SandboxRefusalWitness     string
	RefusedSocketPaths        []string
	RefusedConnectPorts       []string
	RefusedListenPorts        []string
	RefusedSignals            []string
	UnsandboxedRefusals       []string
}

type SourceFacts struct {
	WorktreeStalePaths        []string
	WorktreeLeftoverPaths     []string
	WorktreeConflictPaths     []string
	EditorConfigMismatch      bool
	SourceAnalysisUnavailable bool
	SyntaxCheckOverridden     bool
	SourceIncludes            map[string]bool
}

type AccessFacts struct {
	WriteRoots                []string
	PathOutsideScope          bool
	PathOutsideScopeByTool    map[string]bool
	ActionHostResources       []string
	ActionHostResourceDenials []string
	HostResourceStatus        map[string]string
	HostResourcePolicy        map[string]string
}

type ContentFacts struct {
	ContentLength            int64
	ContentRoles             []string
	ContentOrigins           []string
	ContentAuthorities       []string
	ContentTrustTiers        []string
	ContentSources           []string
	ContentSegmentCount      int64
	ContentContainsUntrusted bool
	Content                  string
	ContentSet               bool
	PromptInjectionScore     float64
	JailbreakScore           float64
	PIIEntities              []any
	SecretMatches            []any
}

type RejectionFacts struct {
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
	RejectObservation string
}

type MCPFacts struct {
	MCPProviderID         string
	MCPToolName           string
	MCPQualifiedTool      string
	MCPProviderConfigured bool
	MCPProviderEnabled    bool
	MCPCallOK             bool
	MCPErrorCode          string
	MCPSchemaMatched      bool
	MCPFields             map[string]any
	MCPResultText         string
	MCPCatalog            MCPCatalogView
}

type CountersFacts struct {
	FireCount           int64
	RepeatCount         int64
	BreakerCount        int64
	FruitlessSearchRun  int64
	SameCodeRejectRun   int64
	CodeRejectResponses int64
	DeferredUnactivated int64
}

type providerState struct {
	mu                sync.Mutex
	providers         map[string]FactProvider
	computed          map[string]bool
	providerRunning   map[string]chan struct{}
	providerErrors    map[string]error
	mcpSchemaComputed bool
	mcpSchemaError    error
}
