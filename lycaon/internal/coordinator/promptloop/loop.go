package promptloop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/toolpolicy"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workerprogress"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromptLoop runs coordinator/worker tool iteration and LLM completion streaming.
type PromptLoop struct {
	Context    *promptContext
	Model      *modelTurn
	Tools      *toolInvocations
	Batch      *toolBatch
	Closeout   *turnCloseout
	Projection *turnProjection
	Nudges     *turnNudges
	Control    *turnControl
	Inbox      *turnInbox
}

type PromptLoopDeps struct {
	Context    ContextDeps
	Model      ModelDeps
	Tools      ToolsDeps
	Closeout   CloseoutDeps
	Projection ProjectionDeps
	Nudges     NudgesDeps
	Control    ControlDeps
	Inbox      InboxDeps
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
	if err := l.Closeout.capturePromptRunCloseout(ctx, setup.state, result); err != nil {
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
			maxIter, hold = l.Nudges.workerLoopCeiling(ctx, sess, st, baseLim, surfaceID, completed, maxIter)
			if hold {
				l.Nudges.awaitWorkerBudgetAnswer(ctx, sess, st)
				continue
			}
		}
		if completed >= maxIter {
			break
		}
		step := completed
		sentBeforeCall, err := l.Inbox.takeUserSend(ctx, id, st, &userPrompt)
		if err != nil {
			return nil, err
		}
		if err := l.Inbox.takePolicyFeedback(ctx, id, st); err != nil {
			return nil, err
		}
		if err := l.Inbox.takePhaseGuidance(ctx, id, st); err != nil {
			return nil, err
		}
		if sentBeforeCall || !firstModelTurn {
			surfaceID, err = l.Context.refreshCoordinatorSurface(ctx, sess, id, profileID, st, surfaceID)
			if err != nil {
				return nil, err
			}
		}
		firstModelTurn = false
		turnCtx := ctx
		if guard.IsCoordinatorProfile(profileID) && st.coordinatorFrameReady {
			turnCtx = toolpolicy.WithCoordinatorTurnFrame(ctx, &st.coordinatorFrame)
		}
		spend, err := l.Nudges.applySpendCeiling(turnCtx, sess, id, profileID, userPrompt, maxIter, in, st)
		if err != nil {
			return nil, err
		}
		if spend.Finished != nil {
			return spend.Finished, nil
		}
		if l.Control.Deps.WorkerGracefulCancelPending != nil && sess.IsWorkerChild() {
			if reason, pending := l.Control.Deps.WorkerGracefulCancelPending(turnCtx, sess); pending {
				loopExit = loopExitGracefulCancel
				closedHistory, aid, content, cerr := l.Closeout.runEarlyTurnCloseout(turnCtx, sess, id, profileID, userPrompt, st.history, maxIter, TurnCloseoutCanceled, reason, false, in, st)
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
		if l.Control.Deps.AssertRunnable != nil {
			if err := l.Control.Deps.AssertRunnable(turnCtx, id); err != nil {
				if st.lastAssistantID != "" {
					loopExit = loopExitAssertRunnable
					break
				}
				return nil, err
			}
		}

		landingCloseout, err := l.Closeout.applyLandingAdvisories(turnCtx, sess, id, profileID, step, maxIter, spend.Runway, spend.WindDown, st)
		if err != nil {
			return nil, err
		}
		if landingCloseout {
			closeoutNudgeSent = true
		}

		assistantMsg, completion, _, err := l.Model.runAssistantStreamTurn(turnCtx, id, sess, st.history, st, profileID, userPrompt, step, maxIter, in.HostTurn)
		if err != nil {
			if errors.Is(err, ErrLLMTurnTimeout) && strings.TrimSpace(st.lastAssistantID) != "" {
				loopExit = loopExitLLMTimeout
				break
			}
			if errors.Is(err, llm.ErrModelRequestSecretWithheld) {
				retry, werr := l.Nudges.handleSecretWithheldTurn(turnCtx, sess, id, err, st)
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
		if sess.IsWorkerChild() && l.Context.Deps.CommitWorkerContext != nil {
			if err := l.Context.Deps.CommitWorkerContext(turnCtx, id, assistantMsg.ID); err != nil {
				return nil, err
			}
		}
		// Persist worker progress at round boundaries.
		completed = step + 1
		st.turnsRanThisRun++
		if sess.IsWorkerChild() {
			var usage *api.WorkerContextUsage
			if completion != nil {
				usage = l.Nudges.workerContextUsage(turnCtx, sess, completion.Usage.PromptTokens)
			}
			l.Nudges.publishWorkerProgress(turnCtx, sess, st.workerJobID, st.progress().CompleteRound(completed, maxIter, usage), true)
		}
		if sent, sendErr := l.Inbox.takeUserSend(turnCtx, id, st, &userPrompt); sendErr != nil {
			return nil, sendErr
		} else if sent {
			if withdrawErr := l.Nudges.withdrawCoordinatorDraft(
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
		if sent, sendErr := l.Inbox.takeUserSend(turnCtx, id, st, &userPrompt); sendErr != nil {
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
func (l *turnNudges) workerLoopCeiling(ctx context.Context, sess *api.Session, st *promptLoopTurnState, baseLim settings.SessionLimits, surfaceID string, completed, maxIter int) (int, bool) {
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
	if l.Context.Deps.Limits != nil {
		workerLim = l.Context.Deps.Limits(ctx, sess)
	} else {
		workerLim = applyWorkerMaxToolLoops(baseLim, sess)
	}
	next := max(surface.EffectivePromptLoopIterations(workerLim, surfaceID), st.repairCeiling)
	st.progress().SetMaxToolLoops(next)
	st.workerBudget.observe(task, completed > 0 && next > maxIter)
	return next, st.workerBudget.holdDue(completed, next)
}

func (l *turnInbox) takeUserSend(
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
	if l.Closeout.Deps.BeginCloseoutIntent != nil {
		l.Closeout.Deps.BeginCloseoutIntent(ctx, sessionID)
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
	if guard.IsCoordinatorProfile(setup.profileID) && l.Closeout.Deps.CloseoutStallState != nil {
		if retained := l.Closeout.Deps.CloseoutStallState(ctx, setup.sessionID); retained.Active {
			setup.state.closeoutRetry = closeoutRetryState{
				attempt: retained.Attempt, documentAttempt: retained.DocumentAttempt, prevKey: retained.PrevKey, codes: retained.ForcedBy,
			}
		}
	}
	setup.limits = settings.DefaultSessionLimits()
	if l.Context.Deps.Limits != nil {
		setup.limits = l.Context.Deps.Limits(ctx, setup.sess)
	}
	var err error
	setup.surfaceID, err = l.Context.refreshCoordinatorSurface(
		ctx, setup.sess, setup.sessionID, setup.profileID, setup.state, "")
	if err != nil {
		return nil, err
	}
	setup.maxIter = surface.EffectivePromptLoopIterations(setup.limits, setup.surfaceID)
	setup.state.workerJobID = strings.TrimSpace(in.ToolCtx.Identity.WorkerJobID)
	setup.state.workerProgress = workerprogress.NewTracker(l.Nudges.seededWorkerProgress(ctx, setup.sess, setup.state.workerJobID))
	setup.completed = setup.state.progress().ToolLoopsUsed()
	setup.state.progress().SetMaxToolLoops(setup.maxIter)
	setup.state.proseFinish = in.ProseFinish
	if setup.sess.IsWorkerChild() && in.HostSignalID == string(anchor.WorkerCitationGrounding) {
		setup.state.repairCeiling = setup.completed + spawn.WorkerRepairRounds
	}
	return setup, nil
}

func (l *promptContext) refreshCoordinatorSurface(
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
	st.coordinatorFrame.ProjectRootCount = l.projectRootCount(ctx, sess)
	if l.Deps.OverlayRootPaths != nil {
		st.coordinatorFrame.OverlayRootPaths = append([]string(nil), l.Deps.OverlayRootPaths(ctx, sess)...)
	}
	surfaceID := l.resolveTurnProfile(ctx, sess, st.history, st.coordinatorFrame).SurfaceID
	if l.Deps.SetPromptTurnSurface != nil {
		l.Deps.SetPromptTurnSurface(sessionID, surfaceID)
	}
	return surfaceID, nil
}

func (l *turnNudges) seededWorkerProgress(ctx context.Context, sess *api.Session, workerJobID string) workerprogress.Snapshot {
	if l == nil || sess == nil || !sess.IsWorkerChild() || l.Deps.WorkerJob == nil || strings.TrimSpace(workerJobID) == "" {
		return workerprogress.Snapshot{}
	}
	task, ok := l.Deps.WorkerJob(ctx, workerJobID)
	if !ok {
		return workerprogress.Snapshot{}
	}
	return workerprogress.FromTask(task)
}

func (l *turnNudges) publishWorkerProgress(ctx context.Context, sess *api.Session, workerJobID string, snap workerprogress.Snapshot, checkpoint bool) {
	if l == nil || l.Deps.PublishWorkerProgress == nil || sess == nil || !sess.IsWorkerChild() || strings.TrimSpace(workerJobID) == "" {
		return
	}
	l.Deps.PublishWorkerProgress(ctx, workerJobID, snap, checkpoint)
}

// Context usage stays absent until a model response reports prompt tokens.
func (l *turnNudges) workerContextUsage(ctx context.Context, sess *api.Session, promptTokens int) *api.WorkerContextUsage {
	if promptTokens <= 0 {
		return nil
	}
	usage := &api.WorkerContextUsage{PromptTokens: promptTokens}
	if l.Model.Deps.CompactionConfig != nil {
		cc := l.Model.Deps.CompactionConfig(ctx, sess)
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
