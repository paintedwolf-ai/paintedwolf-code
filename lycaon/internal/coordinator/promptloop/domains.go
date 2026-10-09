package promptloop

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"time"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/jsonshape"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

type ContextDeps struct {
	ObservePrompt                   func(sessionID string) func(inject.CoordinatorTurnFrame)
	Limits                          func(context.Context, *api.Session) settings.SessionLimits
	Tools                           tools.ToolRegistry
	RootSessionID                   func(ctx context.Context, sessionID string) string
	Policy                          toolpolicy.Engine
	CommitWorkerContext             func(context.Context, string, string) error
	BuildMessages                   func(ctx context.Context, sess *api.Session, history []api.Message, frame *inject.CoordinatorTurnFrame) ([]api.Message, error)
	ToolProcedures                  func(ctx context.Context, sess *api.Session, profileID string, offered []string) (string, error)
	CoordinatorSurfaceActivityLabel func(surfaceID string) string
	WebSearchEnabled                func() bool
	CoordinatorFrame                inject.CoordinatorTurnFrameSource
	CoordinatorPostureRules         func(ctx context.Context, sess *api.Session) ([]string, error)
	OverlayRootPaths                func(ctx context.Context, sess *api.Session) []string
	ImplementSessionState           func(ctx context.Context, sess *api.Session) surface.ImplementSessionState
	RefreshToolContext              func(ctx context.Context, sess *api.Session, machine inject.Machine) (tools.ToolContext, error)
	MCPAlwaysLoad                   func(context.Context, *api.Session) map[string]bool
	SetPromptTurnSurface            func(sessionID, surfaceID string)
	PromptTurnSurface               func(sessionID string) string
	LoadedTools                     func(sessionID string) map[string]bool
	ToolObserved                    func(ctx context.Context, sess *api.Session, toolCtx tools.ToolContext, tool string)
	LiveResources                   func(sessionID string) toolcontract.ResourcePresence
	ProjectRootCount                func(ctx context.Context, sess *api.Session) int
}

type ModelDeps struct {
	LLM                              modelcall.LLMClient
	LLMService                       *llm.Service
	Cost                             cost.CostTracker
	CompactionConfig                 func(ctx context.Context, sess *api.Session) compaction.CompactionConfig
	RecordCompactionTokenObservation func(sessionID string, reportedPromptTokens, transcriptEstimate int)
	CompactionTokenCalibration       func(sessionID string) compaction.PromptTokenCalibration
}

type ToolsDeps struct {
	Invocations                 invocation.Recorder
	VisualStore                 visual.Store
	AgentPresence               *agentpresence.Tracker
	DesignateProjectCover       func(ctx context.Context, projectID, rootSessionID, artifactID string) error
	BlockPlane                  *toolfeedback.BlockPlane
	BeforeToolRun               func(ctx context.Context, sess *api.Session, history []api.Message, userPrompt, tool string, args map[string]any) (output string, skipRun bool, err error)
	AfterToolRun                func(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, succeeded bool, out *tools.ToolInvocationOut) string
	EnrichToolOutput            func(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, raised guidance.ToolResultFacts, doomCompletionCountAfter int) (string, guidance.ToolResultFacts)
	InFlightWorkerRosterNote    func(ctx context.Context, sess *api.Session) string
	DataDir                     string
	ReloadHistory               func(ctx context.Context, sessionID string, sess *api.Session, surfaceID string) ([]api.Message, error)
	CompactOversizedToolResults func(ctx context.Context, sessionID string, sess *api.Session) error
	CompactToolWire             func(ctx context.Context, sess *api.Session, toolName, content string, opts compaction.CompactToolWireOpts) (string, *api.CompactedChunkMeta)
	HeldCalls                   HeldCallRunner
	HeldCallBudget              time.Duration
	CommitEvidenceToolResult    func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (handle string, patchedContent string, err error)
	RecordSourceRunEvidence     func(ctx context.Context, sessionID string, sess *api.Session, toolName string, run tools.SourceRunCapture)
	ConfirmVerifyResult         func(sess *api.Session, content string) string
}

type CloseoutDeps struct {
	RejectFmt                           *guidance.StaticRejectFormatter
	HintConfig                          *guidance.HintConfig
	EvaluateContentAnchor               func(ctx context.Context, sess *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (reject *guidance.Refusal, blocked bool, content string, transformed bool)
	EvaluateCloseoutBlock               func(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error)
	TurnCloseoutNudge                   TurnCloseoutNudge
	IterationRunwayNudge                IterationRunwayNudge
	BeforeFinishNoToolTurn              func(ctx context.Context, sess *api.Session, history []api.Message, userPrompt, lastAssistant, surfaceID string, workersIdle bool, turnTools []string, invokeAllowed bool) (reject *guidance.Refusal, block bool)
	ProseCitationGrounding              func(ctx context.Context, sess *api.Session, history []api.Message, userPrompt, prose, surfaceID string) *api.CitationGrounding
	CitationRoots                       func(ctx context.Context, sess *api.Session) evidence.CitationRoots
	OnGroundedSynthesisAccepted         func(ctx context.Context, sess *api.Session, sessionID string)
	EvidenceLedger                      guidance.CloseoutEvidenceReader
	MaxCloseoutCitationGroundingRetries int
	RenderHostKick                      func(ctx context.Context, kickID string, data map[string]any) (string, error)
	BeginCloseoutIntent                 func(ctx context.Context, sessionID string)
	RecordGroundingFriction             func(ctx context.Context, sessionID string) guidance.GroundingFriction
	NoteCloseoutGroundingReject         func(ctx context.Context, sessionID, code, offenderKey, draftedContent string, unread []jsonshape.Issue) (attempt int, prevKey string)
	NoteCoordinatorToolTurn             func(ctx context.Context, sessionID string) (tripped bool)
	CloseoutStallState                  func(ctx context.Context, sessionID string) guidance.RetainedCloseout
	ClearCloseoutStall                  func(ctx context.Context, sessionID string)
	AssembleLedgerCloseout              func(ctx context.Context, sessionID, surfaceID string, forcedBy []string, draftedContent string, retryCount int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding)
	CheckRunReportDocument              func(ctx context.Context, sessionID string, report guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error)
}

type ProjectionDeps struct {
	Events                   *events.Publisher
	RedactMessageForStorage  func(ctx context.Context, msg api.Message) (api.Message, bool)
	AppendMessages           func(ctx context.Context, sessionID string, msgs ...api.Message) error
	Streams                  MessageStreams
	OnToolReject             func(ctx context.Context, sessionID, toolCallID, code, content string, facts guidance.ToolResultFacts)
	AnnouncePendingToolAsk   func(ctx context.Context, sessionID string)
	UpdateMessage            func(ctx context.Context, sessionID, messageID string, msg api.Message) error
	AdmitModelResponse       func(ctx context.Context, sessionID, attemptID string) error
	SettleModelOutput        func(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error)
	MarkModelOutputProjected func(ctx context.Context, outputID string) error
	CheckpointTurn           func(ctx context.Context, turnID, attemptID string, phase store.TurnPhase, checkpointJSON string) error
	AppendDraftVersion       func(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error)
	CountDraftVersions       func(ctx context.Context, sessionID, slotID string) (int, error)
}

type NudgesDeps struct {
	DoomLoop                  DoomLoopGuard
	FormatDoomLoopReject      func(ctx context.Context, sessionID, tool string, args map[string]any, count int, repeatedCode string) (*guidance.Refusal, error)
	EscalateRepeatedCode      func(ctx context.Context, sessionID, tool string, original *guidance.Refusal) *guidance.Refusal
	CheckSpendCeiling         func(ctx context.Context, sessionID string, sess *api.Session) (SpendCeilingCheck, error)
	IsSpendCeiling            func(error) bool
	SecretWithheldNudge       SecretWithheldNudge
	SurveyStreakNudge         SurveyStreakNudge
	SpendRunwayNudge          SpendRunwayNudge
	SpendSoftStopNudge        SpendSoftStopNudge
	PublishWorkerProgress     func(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool)
	WorkerJob                 func(ctx context.Context, workerJobID string) (*api.WorkerTask, bool)
	WorkerBudgetRaisedNudge   func(ctx context.Context, sess *api.Session, used, max int) HostNudge
	WorkerBudgetDeclinedNudge func(ctx context.Context, sess *api.Session, used, max int) HostNudge
	WorkerBudgetAnswerWait    time.Duration
}

type ControlDeps struct {
	HumanApprovalAwaiting       func(ctx context.Context, sessionID string) bool
	HostObligationHeld          func(ctx context.Context, sessionID string) bool
	ParkBlockedLiveCommands     func(ctx context.Context, sessionID string) bool
	AssertRunnable              func(ctx context.Context, sessionID string) error
	WorkerGracefulCancelPending func(ctx context.Context, sess *api.Session) (reason string, pending bool)
	ReconcileCoordinatorBatch   func(ctx context.Context, sessionID string)
}

type InboxDeps struct {
	TakePolicyFeedback func(context.Context, string) ([]api.Message, error)
	TakeUserSend       func(ctx context.Context, sessionID string) ([]api.Message, error)
	TakePhaseGuidance  func(ctx context.Context, sessionID string) ([]api.Message, error)
}

type promptContext struct {
	Deps     ContextDeps
	Closeout *turnCloseout
}

type modelTurn struct {
	Deps       ModelDeps
	Closeout   *turnCloseout
	Context    *promptContext
	Inbox      *turnInbox
	Nudges     *turnNudges
	Projection *turnProjection
	Tools      *toolInvocations
}

type toolInvocations struct {
	Deps       ToolsDeps
	Batch      *toolBatch
	Closeout   *turnCloseout
	Context    *promptContext
	Control    *turnControl
	Nudges     *turnNudges
	Projection *turnProjection
}

type toolBatch struct {
	Closeout   *turnCloseout
	Context    *promptContext
	Control    *turnControl
	Nudges     *turnNudges
	Projection *turnProjection
	Tools      *toolInvocations
}

type turnCloseout struct {
	Deps       CloseoutDeps
	Batch      *toolBatch
	Context    *promptContext
	Model      *modelTurn
	Nudges     *turnNudges
	Projection *turnProjection
	Tools      *toolInvocations
}

type turnProjection struct {
	Deps     ProjectionDeps
	Closeout *turnCloseout
	Nudges   *turnNudges
}

type turnNudges struct {
	Deps       NudgesDeps
	Closeout   *turnCloseout
	Context    *promptContext
	Control    *turnControl
	Model      *modelTurn
	Projection *turnProjection
}

type turnControl struct {
	Deps ControlDeps
}

type turnInbox struct {
	Deps     InboxDeps
	Closeout *turnCloseout
}

func NewPromptLoop(deps PromptLoopDeps) *PromptLoop {
	context := &promptContext{Deps: deps.Context}
	model := &modelTurn{Deps: deps.Model}
	tools := &toolInvocations{Deps: deps.Tools}
	batch := &toolBatch{}
	closeout := &turnCloseout{Deps: deps.Closeout}
	projection := &turnProjection{Deps: deps.Projection}
	nudges := &turnNudges{Deps: deps.Nudges}
	control := &turnControl{Deps: deps.Control}
	inbox := &turnInbox{Deps: deps.Inbox}
	context.Closeout = closeout
	model.Closeout = closeout
	model.Context = context
	model.Inbox = inbox
	model.Nudges = nudges
	model.Projection = projection
	model.Tools = tools
	tools.Batch = batch
	tools.Closeout = closeout
	tools.Context = context
	tools.Control = control
	tools.Nudges = nudges
	tools.Projection = projection
	batch.Closeout = closeout
	batch.Context = context
	batch.Control = control
	batch.Nudges = nudges
	batch.Projection = projection
	batch.Tools = tools
	closeout.Batch = batch
	closeout.Context = context
	closeout.Model = model
	closeout.Nudges = nudges
	closeout.Projection = projection
	closeout.Tools = tools
	projection.Closeout = closeout
	projection.Nudges = nudges
	nudges.Closeout = closeout
	nudges.Context = context
	nudges.Control = control
	nudges.Model = model
	nudges.Projection = projection
	inbox.Closeout = closeout
	return &PromptLoop{Context: context, Model: model, Tools: tools, Batch: batch, Closeout: closeout, Projection: projection, Nudges: nudges, Control: control, Inbox: inbox}
}
