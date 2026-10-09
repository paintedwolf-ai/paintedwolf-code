package promptloop

import (
	"github.com/lycaon/lycaon/internal/toolfeedback"

	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
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
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromptLoop runs coordinator/worker tool iteration and LLM completion streaming.
type PromptLoop struct {
	Deps PromptLoopDeps
}

type PromptLoopDeps struct {
	// ObservePrompt captures wake ordering before context reads and acknowledges the delivered frame.
	ObservePrompt func(sessionID string) func(inject.CoordinatorTurnFrame)
	Limits        func(context.Context, *api.Session) settings.SessionLimits
	LLM           modelcall.LLMClient
	LLMService    *llm.Service
	Cost          cost.CostTracker
	Events        *events.Publisher
	Tools         tools.ToolRegistry
	Invocations   invocation.Recorder
	VisualStore   visual.Store
	// AgentPresence receives what each call does to project files.
	AgentPresence *agentpresence.Tracker
	// DesignateProjectCover selects the latest successful render.
	DesignateProjectCover func(ctx context.Context, projectID, rootSessionID, artifactID string) error
	RootSessionID         func(ctx context.Context, sessionID string) string
	Policy                toolpolicy.Engine
	DoomLoop              DoomLoopGuard
	RejectFmt             *guidance.StaticRejectFormatter
	BlockPlane            *toolfeedback.BlockPlane
	HintConfig            *guidance.HintConfig
	FormatDoomLoopReject  func(ctx context.Context, sessionID, tool string, args map[string]any, count int, repeatedCode string) (*guidance.Refusal, error)
	// EscalateRepeatedCode evaluates repeated rejection codes after recording the outcome.
	EscalateRepeatedCode func(ctx context.Context, sessionID, tool string, original *guidance.Refusal) *guidance.Refusal
	// EvaluateContentAnchor returns a structured refusal for blocked model content.
	EvaluateContentAnchor func(ctx context.Context, sess *api.Session, anchor string, segments []oar.ContentSegment, tool string, args map[string]any) (reject *guidance.Refusal, blocked bool, content string, transformed bool)
	// EvaluateCloseoutBlock consumes citation facts; a nil decision allows closeout.
	EvaluateCloseoutBlock func(ctx context.Context, sess *api.Session, gc *oar.GuardContext) (*oar.Decision, error)
	BeforeToolRun         func(ctx context.Context, sess *api.Session, history []api.Message, userPrompt, tool string, args map[string]any) (output string, skipRun bool, err error)
	AfterToolRun          func(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, succeeded bool, out *tools.ToolInvocationOut) string
	// HumanApprovalAwaiting parks the turn at a pending human review.
	HumanApprovalAwaiting func(ctx context.Context, sessionID string) bool
	// HostObligationHeld ends tool iteration while the host holds the phase,
	// except for the person's turns on the await_host surface.
	HostObligationHeld func(ctx context.Context, sessionID string) bool
	// ParkBlockedLiveCommands arms a process wait when repetition ends the turn.
	ParkBlockedLiveCommands func(ctx context.Context, sessionID string) bool
	// EnrichToolOutput appends host banners and returns the codes they raised.
	EnrichToolOutput                func(ctx context.Context, sess *api.Session, tool string, args map[string]any, output string, raised guidance.ToolResultFacts, doomCompletionCountAfter int) (string, guidance.ToolResultFacts)
	InFlightWorkerRosterNote        func(ctx context.Context, sess *api.Session) string
	CommitWorkerContext             func(context.Context, string, string) error
	BuildMessages                   func(ctx context.Context, sess *api.Session, history []api.Message, frame *inject.CoordinatorTurnFrame) ([]api.Message, error)
	ToolProcedures                  func(ctx context.Context, sess *api.Session, profileID string, offered []string) (string, error)
	ConfigRoot                      string
	CoordinatorSurfaceActivityLabel func(surfaceID string) string
	DataDir                         string
	WebSearchEnabled                func() bool
	CoordinatorFrame                inject.CoordinatorTurnFrameSource
	// CoordinatorPostureRules captures posture policy for one iteration.
	CoordinatorPostureRules func(ctx context.Context, sess *api.Session) ([]string, error)
	OverlayRootPaths        func(ctx context.Context, sess *api.Session) []string
	ImplementSessionState   func(ctx context.Context, sess *api.Session) surface.ImplementSessionState
	RedactMessageForStorage func(ctx context.Context, msg api.Message) (api.Message, bool)
	AppendMessages          func(ctx context.Context, sessionID string, msgs ...api.Message) error
	// TakeUserSend returns the durable continuation reserved for this loop.
	TakePolicyFeedback          func(context.Context, string) ([]api.Message, error)
	TakeUserSend                func(ctx context.Context, sessionID string) ([]api.Message, error)
	TakePhaseGuidance           func(ctx context.Context, sessionID string) ([]api.Message, error)
	Streams                     MessageStreams
	CheckSpendCeiling           func(ctx context.Context, sessionID string, sess *api.Session) (SpendCeilingCheck, error)
	IsSpendCeiling              func(error) bool
	AssertRunnable              func(ctx context.Context, sessionID string) error
	RefreshToolContext          func(ctx context.Context, sess *api.Session, machine inject.Machine) (tools.ToolContext, error)
	TurnCloseoutNudge           TurnCloseoutNudge
	SecretWithheldNudge         SecretWithheldNudge
	IterationRunwayNudge        IterationRunwayNudge
	SurveyStreakNudge           SurveyStreakNudge
	SpendRunwayNudge            SpendRunwayNudge
	SpendSoftStopNudge          SpendSoftStopNudge
	WorkerGracefulCancelPending func(ctx context.Context, sess *api.Session) (reason string, pending bool)
	OnToolReject                func(ctx context.Context, sessionID, toolCallID, code, content string, facts guidance.ToolResultFacts)
	// AnnouncePendingToolAsk appends an ordered pending marker.
	AnnouncePendingToolAsk      func(ctx context.Context, sessionID string)
	HasActiveWorkflow           func(ctx context.Context, sessionID string) bool
	ReloadHistory               func(ctx context.Context, sessionID string, sess *api.Session, surfaceID string) ([]api.Message, error)
	CompactOversizedToolResults func(ctx context.Context, sessionID string, sess *api.Session) error
	// CompactToolWire spills oversized payloads before storage and streaming.
	// Commit stamps select the payload classifier.
	CompactToolWire                  func(ctx context.Context, sess *api.Session, toolName, content string, opts compaction.CompactToolWireOpts) (string, *api.CompactedChunkMeta)
	CompactionConfig                 func(ctx context.Context, sess *api.Session) compaction.CompactionConfig
	MCPAlwaysLoad                    func(context.Context, *api.Session) map[string]bool
	RecordCompactionTokenObservation func(sessionID string, reportedPromptTokens, transcriptEstimate int)
	CompactionTokenCalibration       func(sessionID string) compaction.PromptTokenCalibration
	// invokeAllowed is false on a closeout turn.
	BeforeFinishNoToolTurn func(ctx context.Context, sess *api.Session, history []api.Message, userPrompt, lastAssistant, surfaceID string, workersIdle bool, turnTools []string, invokeAllowed bool) (reject *guidance.Refusal, block bool)
	ProseCitationGrounding func(ctx context.Context, sess *api.Session, history []api.Message, userPrompt, prose, surfaceID string) *api.CitationGrounding
	// CitationRoots returns the structured roots used by evidence path resolution.
	CitationRoots             func(ctx context.Context, sess *api.Session) evidence.CitationRoots
	UpdateMessage             func(ctx context.Context, sessionID, messageID string, msg api.Message) error
	ProjectLiveModelOutput    func(ctx context.Context, out store.LiveModelOutput) error
	AdmitModelResponse        func(ctx context.Context, sessionID, attemptID string) error
	SettleModelOutput         func(ctx context.Context, out store.ModelOutput) (store.ModelOutput, error)
	MarkModelOutputProjected  func(ctx context.Context, outputID string) error
	CheckpointTurn            func(ctx context.Context, turnID, attemptID string, phase store.TurnPhase, checkpointJSON string) error
	AppendDraftVersion        func(ctx context.Context, sessionID, slotID, body, outcomeCode string) (int, error)
	CountDraftVersions        func(ctx context.Context, sessionID, slotID string) (int, error)
	SetPromptTurnSurface      func(sessionID, surfaceID string)
	PromptTurnSurface         func(sessionID string) string
	ReconcileCoordinatorBatch func(ctx context.Context, sessionID string)
	// LoadedTools returns the tools the turn ledger loaded beyond the surface floor.
	LoadedTools func(sessionID string) map[string]bool
	// ToolObserved runs after a tool call settles successfully; the host may
	// pre-read a skill the tool triggers for the rest of the turn.
	ToolObserved func(ctx context.Context, sess *api.Session, toolCtx tools.ToolContext, tool string)
	// HeldCalls runs read-only tools that may outlive their foreground wait.
	HeldCalls HeldCallRunner
	// HeldCallBudget is how long such a call stays in the foreground; zero uses the default.
	HeldCallBudget time.Duration
	// LiveResources reports command-job, page, terminal, and held-call handles for a session.
	LiveResources    func(sessionID string) toolcontract.ResourcePresence
	ProjectRootCount func(ctx context.Context, sess *api.Session) int
	// CommitEvidenceToolResult mints the result's handle; a non-empty
	// artifactID binds that handle onto the stored visual in the same commit.
	CommitEvidenceToolResult func(ctx context.Context, sessionID string, sess *api.Session, toolName string, args map[string]any, content, artifactID string) (handle string, patchedContent string, err error)
	RecordSourceRunEvidence  func(ctx context.Context, sessionID string, sess *api.Session, toolName string, run tools.SourceRunCapture)
	// ConfirmVerifyResult stamps host verification onto a result.
	ConfirmVerifyResult func(sess *api.Session, content string) string
	// PublishWorkerProgress emits a worker progress edge.
	PublishWorkerProgress func(ctx context.Context, workerJobID string, snap workerprogress.Snapshot, checkpoint bool)
	WorkerJob             func(ctx context.Context, workerJobID string) (*api.WorkerTask, bool)
	// WorkerBudgetRaisedNudge tells a worker its coordinator raised its ceiling.
	WorkerBudgetRaisedNudge func(ctx context.Context, sess *api.Session, used, max int) HostNudge
	// WorkerBudgetDeclinedNudge tells a worker its coordinator declined its request.
	WorkerBudgetDeclinedNudge func(ctx context.Context, sess *api.Session, used, max int) HostNudge
	// WorkerBudgetAnswerWait bounds how long a worker's final round waits for
	// its coordinator to answer an open budget request; zero does not wait.
	WorkerBudgetAnswerWait time.Duration
	// OnGroundedSynthesisAccepted sets the in-turn synthesis latch and batch_phase=closed.
	OnGroundedSynthesisAccepted         func(ctx context.Context, sess *api.Session, sessionID string)
	EvidenceLedger                      guidance.CloseoutEvidenceReader
	MaxCloseoutCitationGroundingRetries int
	RenderHostKick                      func(ctx context.Context, kickID string, data map[string]any) (string, error)
	// BeginCloseoutIntent retires retry state when new user direction enters the loop.
	BeginCloseoutIntent func(ctx context.Context, sessionID string)
	// RecordGroundingFriction shares the cycle budget with workers.
	RecordGroundingFriction func(ctx context.Context, sessionID string) guidance.GroundingFriction
	// NoteCloseoutGroundingReject updates retry state retained across prompt executions.
	NoteCloseoutGroundingReject func(ctx context.Context, sessionID, code, offenderKey, draftedContent string, unread []jsonshape.Issue) (attempt int, prevKey string)
	// NoteCoordinatorToolTurn advances the closeout-stall fuse.
	NoteCoordinatorToolTurn func(ctx context.Context, sessionID string) (tripped bool)
	// CloseoutStallState reads retry state retained across prompt executions.
	CloseoutStallState func(ctx context.Context, sessionID string) guidance.RetainedCloseout
	// ClearCloseoutStall clears citation recovery without replenishing budgets.
	ClearCloseoutStall     func(ctx context.Context, sessionID string)
	AssembleLedgerCloseout func(ctx context.Context, sessionID, surfaceID string, forcedBy []string, draftedContent string, retryCount int) (guidance.CoordinatorCompletionReport, *api.CitationGrounding)
	// CheckRunReportDocument lists the document requirements a closeout that
	// delivers its workflow run's report fails.
	CheckRunReportDocument func(ctx context.Context, sessionID string, report guidance.CoordinatorCompletionReport) ([]guidance.ReportDocumentIssue, error)
}
type PromptRunInput struct {
	SessionID  string
	TurnID     string
	AttemptID  string
	Session    *api.Session
	History    []api.Message
	ProfileID  string
	UserPrompt string
	HostTurn   bool
	// HostSignalID names the host kick a host turn delivers.
	HostSignalID string
	// ProseFinish is a forced closeout for the whole run.
	ProseFinish bool
	ToolCtx     tools.ToolContext
	Machine     inject.Machine
}

// PromptRunResult is the loop output consumed by Manager post-hooks.
type PromptRunResult struct {
	Closeout             *api.Message
	LastAssistantID      string
	LastOutputID         string
	LastAssistantContent string
	TurnTools            []string
	TasksDispatchedCount int
}

type promptLoopSetup struct {
	sess       *api.Session
	sessionID  string
	profileID  string
	userPrompt string
	state      *promptLoopTurnState
	limits     settings.SessionLimits
	surfaceID  string
	maxIter    int
	completed  int
}

func (l *PromptLoop) Run(ctx context.Context, in PromptRunInput) (*PromptRunResult, error) {
	setup, err := l.preparePromptLoop(ctx, in)
	if err != nil {
		return nil, err
	}
	result, err := l.runPreparedPromptLoop(ctx, in, setup)
	if err != nil {
		return nil, err
	}
	if err := (turnCloseout{l}).capturePromptRunCloseout(ctx, setup.state, result); err != nil {
		return nil, err
	}
	return result, nil
}

func (l *PromptLoop) runPreparedPromptLoop(ctx context.Context, in PromptRunInput, setup *promptLoopSetup) (*PromptRunResult, error) {
	sess, id := setup.sess, setup.sessionID
	profileID, userPrompt := setup.profileID, setup.userPrompt
	st := setup.state
	baseLim, surfaceID := setup.limits, setup.surfaceID
	maxIter, completed := setup.maxIter, setup.completed
	var closeoutNudgeSent bool
	var loopExit loopExitKind

	firstModelTurn := true
loop:
	for {
		if sess.IsWorkerChild() {
			var hold bool
			maxIter, hold = l.workerLoopCeiling(ctx, sess, st, baseLim, surfaceID, completed, maxIter)
			if hold {
				turnNudges{l}.awaitWorkerBudgetAnswer(ctx, sess, st)
				continue
			}
		}
		if completed >= maxIter {
			break
		}
		step := completed
		sentBeforeCall, err := l.takeUserSend(ctx, id, st, &userPrompt)
		if err != nil {
			return nil, err
		}
		if err := l.takePolicyFeedback(ctx, id, st); err != nil {
			return nil, err
		}
		if err := l.takePhaseGuidance(ctx, id, st); err != nil {
			return nil, err
		}
		if sentBeforeCall || !firstModelTurn {
			surfaceID, err = l.refreshCoordinatorSurface(ctx, sess, id, profileID, st, surfaceID)
			if err != nil {
				return nil, err
			}
		}
		firstModelTurn = false
		turnCtx := ctx
		if guard.IsCoordinatorProfile(profileID) && st.coordinatorFrameReady {
			turnCtx = toolpolicy.WithCoordinatorTurnFrame(ctx, &st.coordinatorFrame)
		}
		spend, err := turnNudges{l}.applySpendCeiling(turnCtx, sess, id, profileID, userPrompt, maxIter, in, st)
		if err != nil {
			return nil, err
		}
		if spend.Finished != nil {
			return spend.Finished, nil
		}
		if l.Deps.WorkerGracefulCancelPending != nil && sess.IsWorkerChild() {
			if reason, pending := l.Deps.WorkerGracefulCancelPending(turnCtx, sess); pending {
				loopExit = loopExitGracefulCancel
				closedHistory, aid, content, cerr := turnCloseout{l}.runEarlyTurnCloseout(turnCtx, sess, id, profileID, userPrompt, st.history, maxIter, TurnCloseoutCanceled, reason, false, in, st)
				if cerr != nil {
					return nil, cerr
				}
				st.history = closedHistory
				if aid != "" {
					st.lastAssistantID = aid
					st.lastAssistantContent = content
					st.proseFinishDelivered = true
				}
				break loop
			}
		}
		if l.Deps.AssertRunnable != nil {
			if err := l.Deps.AssertRunnable(turnCtx, id); err != nil {
				if st.lastAssistantID != "" {
					loopExit = loopExitAssertRunnable
					break
				}
				return nil, err
			}
		}

		landingCloseout, err := turnCloseout{l}.applyLandingAdvisories(turnCtx, sess, id, profileID, step, maxIter, spend.Runway, spend.WindDown, st)
		if err != nil {
			return nil, err
		}
		if landingCloseout {
			closeoutNudgeSent = true
		}

		assistantMsg, completion, _, err := modelTurn{l}.runAssistantStreamTurn(turnCtx, id, sess, st.history, st, profileID, userPrompt, step, maxIter, in.HostTurn)
		if err != nil {
			if errors.Is(err, ErrLLMTurnTimeout) && strings.TrimSpace(st.lastAssistantID) != "" {
				loopExit = loopExitLLMTimeout
				break
			}
			if errors.Is(err, llm.ErrModelRequestSecretWithheld) {
				retry, werr := turnNudges{l}.handleSecretWithheldTurn(turnCtx, sess, id, err, st)
				if werr != nil {
					return nil, werr
				}
				if retry {
					continue
				}
				loopExit = loopExitSecretWithheld
				break
			}
			return nil, err
		}
		if sess.IsWorkerChild() && l.Deps.CommitWorkerContext != nil {
			if err := l.Deps.CommitWorkerContext(turnCtx, id, assistantMsg.ID); err != nil {
				return nil, err
			}
		}
		// Persist worker progress at round boundaries.
		completed = step + 1
		st.turnsRanThisRun++
		if sess.IsWorkerChild() {
			var usage *api.WorkerContextUsage
			if completion != nil {
				usage = l.workerContextUsage(turnCtx, sess, completion.Usage.PromptTokens)
			}
			l.publishWorkerProgress(turnCtx, sess, st.workerJobID, st.progress().CompleteRound(completed, maxIter, usage), true)
		}
		if sent, sendErr := l.takeUserSend(turnCtx, id, st, &userPrompt); sendErr != nil {
			return nil, sendErr
		} else if sent {
			if withdrawErr := (turnNudges{l}).withdrawCoordinatorDraft(
				context.WithoutCancel(turnCtx), sess, id, st, assistantMsg.Content,
			); withdrawErr != nil {
				return nil, withdrawErr
			}
			continue
		}
		turnResult, err := l.processPromptLoopAssistantTurn(turnCtx, sess, id, in, step, maxIter, surfaceID, userPrompt, st, assistantMsg, completion)
		if err != nil {
			return nil, err
		}
		if sent, sendErr := l.takeUserSend(turnCtx, id, st, &userPrompt); sendErr != nil {
			return nil, sendErr
		} else if sent {
			continue
		}
		if turnResult.continueLoop {
			continue
		}
		if turnResult.breakLoop {
			loopExit = turnResult.exit
			break
		}
	}

	return l.finalizePromptLoopRun(ctx, sess, id, profileID, userPrompt, maxIter, closeoutNudgeSent, loopExit, in, st)
}

// workerLoopCeiling refreshes a worker's round ceiling from its job ledger and
// reports whether an open budget request holds the loop.
func (l *PromptLoop) workerLoopCeiling(ctx context.Context, sess *api.Session, st *promptLoopTurnState, baseLim settings.SessionLimits, surfaceID string, completed, maxIter int) (int, bool) {
	var task *api.WorkerTask
	if l.Deps.WorkerJob != nil {
		if job, ok := l.Deps.WorkerJob(ctx, st.workerJobID); ok && job != nil {
			task = job
			if task.MaxToolLoops > 0 {
				sess.MaxToolLoops = task.MaxToolLoops
			}
		}
	}
	var workerLim settings.SessionLimits
	if l.Deps.Limits != nil {
		workerLim = l.Deps.Limits(ctx, sess)
	} else {
		workerLim = applyWorkerMaxToolLoops(baseLim, sess)
	}
	next := max(surface.EffectivePromptLoopIterations(workerLim, surfaceID), st.repairCeiling)
	st.progress().SetMaxToolLoops(next)
	st.workerBudget.observe(task, completed > 0 && next > maxIter)
	return next, st.workerBudget.holdDue(completed, next)
}

func (l *PromptLoop) takeUserSend(
	ctx context.Context,
	sessionID string,
	st *promptLoopTurnState,
	userPrompt *string,
) (bool, error) {
	if l == nil || l.Deps.TakeUserSend == nil || st == nil {
		return false, nil
	}
	messages, err := l.Deps.TakeUserSend(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if len(messages) == 0 {
		return false, nil
	}
	if l.Deps.BeginCloseoutIntent != nil {
		l.Deps.BeginCloseoutIntent(ctx, sessionID)
	}
	st.closeoutRetry = closeoutRetryState{}
	st.history = append(st.history, messages...)
	for _, msg := range messages {
		instruction := strings.TrimSpace(api.MessageUserInstructionContent(msg))
		if instruction == "" {
			continue
		}
		if strings.TrimSpace(*userPrompt) == "" {
			*userPrompt = instruction
		} else {
			*userPrompt = strings.TrimSpace(*userPrompt) + "\n\n" + instruction
		}
	}
	st.lastAssistantID = ""
	st.lastAssistantContent = ""
	st.lastOutputID = ""
	st.proseFinish = false
	st.clearBlockedStreak()
	return true, nil
}

func (l *PromptLoop) preparePromptLoop(ctx context.Context, in PromptRunInput) (*promptLoopSetup, error) {
	if l == nil {
		return nil, fmt.Errorf("prompt loop not configured")
	}
	setup := &promptLoopSetup{
		sess: in.Session, sessionID: in.SessionID, profileID: in.ProfileID,
		userPrompt: in.UserPrompt, state: &promptLoopTurnState{
			history: in.History, machine: in.Machine, turnID: in.TurnID, attemptID: in.AttemptID,
		},
	}
	if guard.IsCoordinatorProfile(setup.profileID) && l.Deps.CloseoutStallState != nil {
		if retained := l.Deps.CloseoutStallState(ctx, setup.sessionID); retained.Active {
			setup.state.closeoutRetry = closeoutRetryState{
				attempt: retained.Attempt, documentAttempt: retained.DocumentAttempt, prevKey: retained.PrevKey, codes: retained.ForcedBy,
			}
		}
	}
	setup.limits = settings.DefaultSessionLimits()
	if l.Deps.Limits != nil {
		setup.limits = l.Deps.Limits(ctx, setup.sess)
	}
	var err error
	setup.surfaceID, err = l.refreshCoordinatorSurface(
		ctx, setup.sess, setup.sessionID, setup.profileID, setup.state, "")
	if err != nil {
		return nil, err
	}
	setup.maxIter = surface.EffectivePromptLoopIterations(setup.limits, setup.surfaceID)
	setup.state.workerJobID = strings.TrimSpace(in.ToolCtx.Identity.WorkerJobID)
	setup.state.workerProgress = workerprogress.NewTracker(l.seededWorkerProgress(ctx, setup.sess, setup.state.workerJobID))
	setup.completed = setup.state.progress().ToolLoopsUsed()
	setup.state.progress().SetMaxToolLoops(setup.maxIter)
	setup.state.proseFinish = in.ProseFinish
	if setup.sess.IsWorkerChild() && in.HostSignalID == string(anchor.WorkerCitationGrounding) {
		setup.state.repairCeiling = setup.completed + spawn.WorkerRepairRounds
	}
	return setup, nil
}

func (l *PromptLoop) refreshCoordinatorSurface(
	ctx context.Context,
	sess *api.Session,
	sessionID, profileID string,
	st *promptLoopTurnState,
	current string,
) (string, error) {
	if !guard.IsCoordinatorProfile(profileID) {
		st.coordinatorFrame = inject.CoordinatorTurnFrame{}
		inject.StampMachine(&st.coordinatorFrame, st.machine)
		st.coordinatorFrameReady = st.machine.Compiled()
		return current, nil
	}
	st.coordinatorFrame = inject.CoordinatorTurnFrame{}
	st.observePrompt = nil
	if l.Deps.ObservePrompt != nil {
		st.observePrompt = l.Deps.ObservePrompt(sessionID)
	}
	st.coordinatorFrameReady = true
	if l.Deps.CoordinatorFrame != nil {
		frame, err := l.Deps.CoordinatorFrame.BuildCoordinatorTurnFrame(ctx, sessionID, sess)
		if err != nil {
			return "", err
		}
		inject.StampMachine(&frame, st.machine)
		st.coordinatorFrame = frame
	} else {
		inject.StampMachine(&st.coordinatorFrame, st.machine)
	}
	if l.Deps.CoordinatorPostureRules != nil {
		paths, err := l.Deps.CoordinatorPostureRules(ctx, sess)
		if err != nil {
			return "", err
		}
		st.coordinatorFrame.PostureRules = append([]string(nil), paths...)
	}
	st.coordinatorFrame.ProjectRootCount = modelTurn{l}.projectRootCount(ctx, sess)
	if l.Deps.OverlayRootPaths != nil {
		st.coordinatorFrame.OverlayRootPaths = append([]string(nil), l.Deps.OverlayRootPaths(ctx, sess)...)
	}
	surfaceID := modelTurn{l}.resolveTurnProfile(ctx, sess, st.history, st.coordinatorFrame).SurfaceID
	if l.Deps.SetPromptTurnSurface != nil {
		l.Deps.SetPromptTurnSurface(sessionID, surfaceID)
	}
	return surfaceID, nil
}

func (l *PromptLoop) seededWorkerProgress(ctx context.Context, sess *api.Session, workerJobID string) workerprogress.Snapshot {
	if l == nil || sess == nil || !sess.IsWorkerChild() || l.Deps.WorkerJob == nil || strings.TrimSpace(workerJobID) == "" {
		return workerprogress.Snapshot{}
	}
	task, ok := l.Deps.WorkerJob(ctx, workerJobID)
	if !ok {
		return workerprogress.Snapshot{}
	}
	return workerprogress.FromTask(task)
}

func (l *PromptLoop) publishWorkerProgress(ctx context.Context, sess *api.Session, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) {
	if l == nil || l.Deps.PublishWorkerProgress == nil || sess == nil || !sess.IsWorkerChild() || strings.TrimSpace(workerJobID) == "" {
		return
	}
	l.Deps.PublishWorkerProgress(ctx, workerJobID, snap, checkpoint)
}

// Context usage stays absent until a model response reports prompt tokens.
func (l *PromptLoop) workerContextUsage(ctx context.Context, sess *api.Session, promptTokens int) *api.WorkerContextUsage {
	if promptTokens <= 0 {
		return nil
	}
	usage := &api.WorkerContextUsage{PromptTokens: promptTokens}
	if l.Deps.CompactionConfig != nil {
		cc := l.Deps.CompactionConfig(ctx, sess)
		usage.Window = cc.ModelContextWindow
		usage.CompactionThreshold = cc.CompactionTriggerTokens()
	}
	return usage
}

func applyWorkerMaxToolLoops(lim settings.SessionLimits, sess *api.Session) settings.SessionLimits {
	if sess == nil || strings.TrimSpace(sess.ParentSessionID) == "" {
		return lim
	}
	if sess.MaxToolLoops > 0 {
		lim.MaxIterations = sess.MaxToolLoops
		return lim
	}
	lim.MaxIterations = lim.WorkerToolBudgetDefault
	return lim
}

// MessageStreams provides transient projection and exact replay for the prompt loop.
type MessageStreams interface {
	CacheLive(sessionID, messageID, content string, tokens []string, generatingTokens int)
	CacheReplay(messageID, content string, tokens []string)
	Project(context.Context, string, api.Message) error
	Finish(context.Context, string)
}
