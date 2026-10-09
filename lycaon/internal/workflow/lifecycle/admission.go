package lifecycle

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/observability"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// Admission owns session start serialization and reviewed workflow activation.
type Admission struct {
	Runs        RunReader
	Starts      StartRepository
	Sessions    Sessions
	Resolver    *workflowcatalog.Resolver
	Events      *events.Publisher
	Barrier     StartBarrier
	Controls    *Commands
	Cleanup     *Cleanup
	Publication Publication
	Plans       Plans
	Approvals   Approvals
	Phases      Phases
	Requests    RequestAdmission
	Feedback    FeedbackNotifications
	guards      sync.Map
}

func (m *Admission) Guard(sessionID string) *sync.Mutex {
	guard, _ := m.guards.LoadOrStore(sessionID, &sync.Mutex{})
	return guard.(*sync.Mutex)
}

func (m *Admission) ForgetSession(sessionID string) { m.guards.Delete(sessionID) }

var workflowStartLog = observability.LazyComponent("workflow_start")

// Start creates a running workflow for a session.
func (m *Admission) Start(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest) (*api.WorkflowRun, error) {
	return m.start(ctx, sessionID, req, "")
}

// StartHuman creates a human-authorized workflow run.
func (m *Admission) StartHuman(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest) (*api.WorkflowRun, error) {
	return m.HumanText(ctx, sessionID, req, "")
}

type automaticHumanReplacementKey struct{}

func (m *Admission) HumanText(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest, slashText string) (*api.WorkflowRun, error) {
	ctx = context.WithValue(hostctx.WithHumanWorkflowStart(ctx), automaticHumanReplacementKey{}, true)
	finish := m.beginStartingWorkflowActivity(ctx, sessionID)
	defer finish()
	return m.start(ctx, sessionID, req, slashText)
}

// beginStartingWorkflowActivity covers phase entry before the initial wake turn.
func (m *Admission) beginStartingWorkflowActivity(ctx context.Context, sessionID string) func() {
	if m == nil || m.Events == nil || m.Sessions == nil {
		return func() {}
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return func() {}
	}
	key := sess.ProjectID
	event := api.ActivityEvent{
		ActivityID: uuid.NewString(),
		SessionID:  sessionID,
		Kind:       api.ActivityKindStartingWorkflow,
		Status:     api.ActivityStatusActive,
		StartedAt:  time.Now().UTC(),
	}
	m.Events.PublishActivity(ctx, key, event.SessionID, event)
	var once sync.Once
	return func() {
		once.Do(func() {
			event.Status = api.ActivityStatusDone
			m.Events.PublishActivity(context.WithoutCancel(ctx), key, event.SessionID, event)
		})
	}
}

type initializedWorkflowStart struct {
	Run      *api.WorkflowRun
	Vars     map[string]any
	Mutation runstate.StartMutation
}

func (m *Admission) start(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest, slashText string) (*api.WorkflowRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	humanStart := hostctx.HumanWorkflowStart(ctx) || strings.TrimSpace(slashText) != ""
	ambientStart := hostctx.AmbientAttach(ctx)
	if !humanStart && !ambientStart {
		return nil, runstate.ErrWorkflowStartRequiresHumanApproval
	}
	var started *workflowStartResult
	start := func() error {
		var err error
		started, err = m.startAdmitted(ctx, sessionID, req, slashText, humanStart, ambientStart)
		return err
	}
	var err error
	if m.Barrier != nil && !StopRepair(ctx) {
		err = m.Barrier.WithSessionTreeAdmission(ctx, sessionID, start)
	} else {
		err = start()
	}
	if err != nil {
		return nil, err
	}
	// Curation takes a separate stop admission after the start gate releases.
	m.Requests.NotifyAccepted(ctx, sessionID, started.Request)
	return started.Run, nil
}

type workflowStartResult struct {
	Run     *api.WorkflowRun
	Request string
}

func (m *Admission) startAdmitted(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest, slashText string, humanStart, ambientStart bool) (*workflowStartResult, error) {
	guard := m.Guard(sessionID)
	guard.Lock()
	defer guard.Unlock()
	if strings.TrimSpace(req.OperationID) == "" {
		req.OperationID = uuid.NewString()
	}
	explicitReplacement := strings.TrimSpace(req.ReplaceRunID) != ""
	startDigest, err := runstate.StartDigest(sessionID, req)
	if err != nil {
		return nil, err
	}
	if replayed, found, replayErr := m.Starts.ReplayStart(ctx, req.OperationID, sessionID, startDigest); replayErr != nil || found {
		return &workflowStartResult{Run: replayed}, replayErr
	}
	workflowID := strings.TrimSpace(req.WorkflowID)
	version := strings.TrimSpace(req.WorkflowVersion)
	presetID := strings.TrimSpace(req.PresetID)
	if workflowID == "" || version == "" {
		return nil, workflowdef.ErrUnknownWorkflow
	}
	sess, err := m.Sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if ambientStart && active != nil {
		return nil, runstate.ErrActiveRunExists
	}
	automaticReplacement, _ := ctx.Value(automaticHumanReplacementKey{}).(bool)
	if automaticReplacement && strings.TrimSpace(req.ReplaceRunID) == "" && req.ExpectedRevision == 0 && active != nil {
		req.ReplaceRunID = active.ID
		req.ExpectedRevision = active.Revision
	}
	manifest, err := m.Resolver.ForSession(ctx, sess.WorkspacePath, sessionID, workflowID, version)
	if err != nil {
		return nil, err
	}
	if manifest.Retired {
		return nil, workflowdef.ErrUnknownWorkflow
	}
	if err := m.validateActiveRunForStart(active, runstate.IsAmbientRun(active), manifest, humanStart); err != nil {
		return nil, err
	}
	replacementTarget, err := m.reviewedReplacementTarget(ctx, sessionID, active, req, humanStart)
	if err != nil {
		return nil, err
	}
	initialized, err := m.initializeWorkflowStart(ctx, sess, active, manifest, req, slashText, presetID, workflowID, version, ambientStart)
	if err != nil {
		return nil, err
	}
	run, vars := initialized.Run, initialized.Vars
	initialized.Mutation.AllowReplacementRebase = replacementTarget != nil && !explicitReplacement
	if replacementTarget != nil {
		initialized.Mutation.ReplacementTeardowns, err = m.Cleanup.ForSession(ctx, sessionID, runstate.ExitReasonSupersededByWorkflowStart)
		if err != nil {
			return nil, err
		}
	}
	if err := m.activateStartedRun(ctx, replacementTarget, run, initialized.Mutation, req.OperationID, startDigest); err != nil {
		return nil, err
	}
	m.Feedback.NotifyPending(ctx, run.SessionID, vars)
	if runstate.RequestPending(vars) {
		m.Publication.Publish(ctx, sess, run)
		return &workflowStartResult{Run: run}, nil
	}
	run, err = m.Phases.ActivateInitial(ctx, run, manifest, sess.WorkspacePath, humanStart)
	if err != nil {
		return nil, err
	}
	m.Publication.Publish(ctx, sess, run)
	result := &workflowStartResult{Run: run}
	if humanStart {
		if request, ok := runstate.RequestStateFromVars(vars); ok && request.Status == runstate.RequestStatusResolved {
			result.Request = request.Text
		}
	}
	return result, nil
}

func (m *Admission) initializeWorkflowStart(
	ctx context.Context,
	sess *api.Session,
	active *api.WorkflowRun,
	manifest workflowdef.Manifest,
	req api.StartWorkflowRunRequest,
	slashText, presetID, workflowID, version string,
	activationOnly bool,
) (*initializedWorkflowStart, error) {
	params, err := workflowdef.MergeStartParams(manifest, presetID, req)
	if err != nil {
		return nil, err
	}
	run := &api.WorkflowRun{
		ID: runstate.OperationRunID(req.OperationID), SessionID: sess.ID, ProjectID: sess.ProjectID,
		WorkflowID: manifest.ID, WorkflowVersion: manifest.Version, AttachPolicy: string(manifest.Attach.Policy),
		Status: api.WorkflowRunStatusRunning, CurrentPhase: manifest.FirstPhase(),
	}
	if workflowdef.SupportsBlueprints(manifest) {
		if m.Plans == nil {
			return nil, runstate.ErrPlanDraftRequired
		}
		fixedPath := ""
		if manifest.Blueprint != nil {
			fixedPath = strings.TrimSpace(manifest.Blueprint.Path)
		}
		blueprintPath, err := m.Plans.ResolvePlanForStart(ctx, req, sess.ProjectID, fixedPath, manifest.ID, sess.ID, slashText)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(blueprintPath) == "" {
			return nil, runstate.ErrPlanDraftRequired
		}
		run.BlueprintPath = blueprintPath
		workflowStartLog.Info("linked draft blueprint to workflow run",
			"session_id", sess.ID, "workflow_id", run.WorkflowID,
			"blueprint_path", blueprintPath, "blueprint_title", req.BlueprintTitle)
	}
	baselinePosture := m.Controls.BaselinePosture(ctx, m.Controls.RootRun(ctx, active), sess.Posture)
	vars := runstate.InitializeRequestVars(nil, manifest.Request, req.Request, activationOnly)
	if !runstate.RequestPending(vars) {
		vars, err = m.Phases.InitializeVars(ctx, sess, run, manifest, vars)
		if err != nil {
			return nil, err
		}
	}
	vars = runstate.ApplyMergedParams(vars, params)
	vars = runstate.ApplyAutoApproveEffects(vars, manifest)
	vars = runstate.StampDepthParamSkips(vars, manifest)
	vars = runstate.RecordWorkflowArtifactParams(vars, manifest)
	if manifest.Controls.ContentReview != nil {
		vars = runstate.ApplyPhaseContentReviewVars(vars, workflowdef.PhaseDef{ContentReview: manifest.Controls.ContentReview})
	}
	vars = runstate.SaveBaselinePosture(vars, baselinePosture)
	if presetID != "" {
		vars = runstate.SetHostVar(vars, "workflow.preset_id", presetID)
	}
	if m.Resolver.SessionScoped(ctx, sess.WorkspacePath, sess.ID, workflowID, version) {
		vars = runstate.SetHostVar(vars, "workflow_compose_summary_id", workflowdef.ManifestKey(workflowID, version))
	}
	terminalSink := false
	if def, ok := manifest.PhaseByID(run.CurrentPhase); ok {
		if !runstate.RequestPending(vars) {
			vars, err = m.Approvals.AutoApproveOnPhase(ctx, run, manifest, def, vars)
			if err != nil {
				return nil, err
			}
			now := time.Now().UTC()
			terminalSink = runstate.CompleteTerminalPhaseEntry(run, def, now)
			if terminalSink {
				run.UpdatedAt = now
			}
		}
	}
	messages, startMessage := runstate.StartBoundaryMessages(run, slashText, req.OperationID)
	if phaseID, feedback, pending := runstate.PendingPromptFromVars(vars); pending {
		var feedbackMessage *api.Message
		vars, feedbackMessage = runstate.BuildFeedbackAnnouncement(run, phaseID, feedback, vars,
			runstate.OperationMessageID(req.OperationID, "feedback:"+phaseID))
		if feedbackMessage != nil {
			messages = append(messages, *feedbackMessage)
		}
	}
	if terminalSink {
		boundary := runstate.NewCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
		run.EndMessageID = boundary.ID
		messages = append(messages, boundary)
	}
	run.StartMessageID = api.SpanScrollAnchor(messages, startMessage)
	mutation := runstate.StartMutation{ProjectDir: sess.WorkspacePath, Vars: vars, Messages: messages,
		Posture: runstate.MutationPosture(run, vars, workflowdef.StartPosture(manifest, run.CurrentPhase))}
	return &initializedWorkflowStart{Run: run, Vars: vars, Mutation: mutation}, nil
}

func (m *Admission) activateStartedRun(
	ctx context.Context,
	replacementTarget, run *api.WorkflowRun,
	mutation runstate.StartMutation,
	operationID, inputDigest string,
) error {
	replacedRuns, err := m.Starts.ActivateStart(ctx, operationID, inputDigest, replacementTarget, run, mutation)
	if err != nil {
		return err
	}
	for i := range replacedRuns {
		m.Controls.AfterCanceled(ctx, &replacedRuns[i], runstate.ExitReasonSupersededByWorkflowStart)
	}
	return nil
}

func (m *Admission) reviewedReplacementTarget(ctx context.Context, sessionID string, active *api.WorkflowRun, req api.StartWorkflowRunRequest, humanStart bool) (*api.WorkflowRun, error) {
	if active == nil {
		if strings.TrimSpace(req.ReplaceRunID) != "" || req.ExpectedRevision > 0 {
			return nil, fmt.Errorf("%w: session %s has no active workflow", runstate.ErrRevisionConflict, sessionID)
		}
		return nil, nil
	}
	if !humanStart {
		return active, nil
	}
	runID := strings.TrimSpace(req.ReplaceRunID)
	if runID == "" || req.ExpectedRevision < 1 {
		if runstate.IsAmbientRun(m.Controls.RootRun(ctx, active)) {
			return active, nil
		}
		return nil, runstate.ErrWorkflowReplacementTargetRequired
	}
	target, err := m.Runs.Get(ctx, runID)
	if err != nil || target == nil || target.SessionID != sessionID || runstate.IsTerminal(target.Status) {
		return nil, fmt.Errorf("%w: reviewed replacement run %s is not active", runstate.ErrRevisionConflict, runID)
	}
	if target.Revision != req.ExpectedRevision {
		return nil, fmt.Errorf("%w: run %s expected revision %d, actual %d", runstate.ErrRevisionConflict, target.ID, req.ExpectedRevision, target.Revision)
	}
	return target, nil
}

func (m *Admission) validateActiveRunForStart(active *api.WorkflowRun, activeAmbient bool, incoming workflowdef.Manifest, humanStart bool) error {
	if active == nil {
		return nil
	}
	if activeAmbient {
		if incoming.Attach.Policy == workflowdef.AttachPolicySessionCreate {
			return runstate.ErrActiveRunExists
		}
		return nil
	}
	if humanStart {
		return nil
	}
	return runstate.ErrActiveRunExists
}
