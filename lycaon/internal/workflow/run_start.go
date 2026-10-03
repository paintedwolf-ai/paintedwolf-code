package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/hostctx"
	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/internal/session"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

const exitReasonSupersededByWorkflowStart = "superseded_by_workflow_start"

var workflowStartLog = observability.LazyComponent("workflow_start")

// Start creates a running workflow for a session.
func (m *RunManager) Start(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest) (*api.WorkflowRun, error) {
	return m.start(ctx, sessionID, req, "")
}

// StartHuman creates a human-authorized workflow run.
func (m *RunManager) StartHuman(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest) (*api.WorkflowRun, error) {
	return m.startHuman(ctx, sessionID, req, "")
}

type automaticHumanReplacementKey struct{}

func (m *RunManager) startHuman(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest, slashText string) (*api.WorkflowRun, error) {
	ctx = context.WithValue(hostctx.WithHumanWorkflowStart(ctx), automaticHumanReplacementKey{}, true)
	finish := m.beginStartingWorkflowActivity(ctx, sessionID)
	defer finish()
	return m.start(ctx, sessionID, req, slashText)
}

// beginStartingWorkflowActivity covers phase entry before the initial wake turn.
func (m *RunManager) beginStartingWorkflowActivity(ctx context.Context, sessionID string) func() {
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
	Mutation workflowStartMutation
}

func (m *RunManager) start(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest, slashText string) (*api.WorkflowRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	humanStart := hostctx.HumanWorkflowStart(ctx) || strings.TrimSpace(slashText) != ""
	ambientStart := hostctx.AmbientAttach(ctx)
	if !humanStart && !ambientStart {
		return nil, ErrWorkflowStartRequiresHumanApproval
	}
	var started *workflowStartResult
	start := func() error {
		var err error
		started, err = m.startAdmitted(ctx, sessionID, req, slashText, humanStart, ambientStart)
		return err
	}
	var err error
	if m.SessionAdmission != nil && !workflowStopRepair(ctx) {
		err = m.SessionAdmission.WithSessionTreeAdmission(ctx, sessionID, start)
	} else {
		err = start()
	}
	if err != nil {
		return nil, err
	}
	// Curation takes a separate stop admission after the start gate releases.
	m.notifyRequestAccepted(ctx, sessionID, started.Request)
	return started.Run, nil
}

type workflowStartResult struct {
	Run     *api.WorkflowRun
	Request string
}

func (m *RunManager) startAdmitted(ctx context.Context, sessionID string, req api.StartWorkflowRunRequest, slashText string, humanStart, ambientStart bool) (*workflowStartResult, error) {
	guard := m.startGuardFor(sessionID)
	guard.Lock()
	defer guard.Unlock()
	if strings.TrimSpace(req.OperationID) == "" {
		req.OperationID = uuid.NewString()
	}
	explicitReplacement := strings.TrimSpace(req.ReplaceRunID) != ""
	startDigest, err := workflowStartDigest(sessionID, req)
	if err != nil {
		return nil, err
	}
	if replayed, found, replayErr := m.Store.ReplayStart(ctx, req.OperationID, sessionID, startDigest); replayErr != nil || found {
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
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if ambientStart && active != nil {
		return nil, ErrActiveRunExists
	}
	automaticReplacement, _ := ctx.Value(automaticHumanReplacementKey{}).(bool)
	if automaticReplacement && strings.TrimSpace(req.ReplaceRunID) == "" && req.ExpectedRevision == 0 && active != nil {
		req.ReplaceRunID = active.ID
		req.ExpectedRevision = active.Revision
	}
	manifest, err := m.manifestForSession(ctx, sess.WorkspacePath, sessionID, workflowID, version)
	if err != nil {
		return nil, err
	}
	if manifest.Retired {
		return nil, workflowdef.ErrUnknownWorkflow
	}
	if err := m.validateActiveRunForStart(active, m.IsAmbientRun(active), manifest, humanStart); err != nil {
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
		initialized.Mutation.ReplacementTeardowns, err = m.activeTeardownIntents(ctx, sessionID, exitReasonSupersededByWorkflowStart)
		if err != nil {
			return nil, err
		}
	}
	if err := m.activateStartedRun(ctx, replacementTarget, run, initialized.Mutation, req.OperationID, startDigest); err != nil {
		return nil, err
	}
	m.notifyFeedbackPending(ctx, run.SessionID, vars)
	if requestPending(vars) {
		m.publish(ctx, sess, run)
		return &workflowStartResult{Run: run}, nil
	}
	run, err = m.activateInitialWorkflowPhase(ctx, run, manifest, sess.WorkspacePath, humanStart)
	if err != nil {
		return nil, err
	}
	m.publish(ctx, sess, run)
	result := &workflowStartResult{Run: run}
	if humanStart {
		if request, ok := requestStateFromVars(vars); ok && request.Status == requestStatusResolved {
			result.Request = request.Text
		}
	}
	return result, nil
}

func (m *RunManager) activateInitialWorkflowPhase(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, projectDir string, wake bool) (*api.WorkflowRun, error) {
	if run.Status == api.WorkflowRunStatusComplete {
		return run, m.ReconcileTerminalRun(ctx, run)
	}
	if def, ok := manifest.PhaseByID(run.CurrentPhase); ok && def.Terminal && !IsTerminal(run.Status) {
		vars, err := m.Store.GetScaffoldVars(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		completeTerminalPhaseEntry(run, def, now)
		run.UpdatedAt = now
		boundary := newCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
		run.EndMessageID = boundary.ID
		if err := m.commitCommandMessages(ctx, run, "activate_initial_terminal", struct{}{}, vars, []api.Message{boundary}, "", workflowWorkerMutation{}, nil); err != nil {
			return nil, err
		}
		return run, m.ReconcileTerminalRun(ctx, run)
	}
	if def, ok := manifest.PhaseByID(run.CurrentPhase); ok && !IsTerminal(run.Status) {
		m.triggerPhaseEnter(ctx, run, projectDir, def)
		if m.PhaseEnterHook != nil {
			m.PhaseEnterHook(ctx, &RunContext{
				SessionID:     run.SessionID,
				RunID:         run.ID,
				WorkflowID:    run.WorkflowID,
				Phase:         run.CurrentPhase,
				PreviousPhase: "",
			}, def)
		}
		if err := m.maybeInvokeOnPhaseEnter(ctx, run, def); err != nil {
			return nil, err
		}
	}
	advanced, err := m.TryAutoAdvanceThroughCommittedGates(ctx, run.ID, len(manifest.Phases))
	if err != nil {
		return nil, err
	}
	run = advanced
	// Human starts receive an initial-phase wake.
	if wake && m.OnPhaseAutoAdvanced != nil && run.Status == api.WorkflowRunStatusRunning {
		m.OnPhaseAutoAdvanced(ctx, run.SessionID, run.ID, "", run.CurrentPhase)
	}
	return run, nil
}

func (m *RunManager) initializeWorkflowStart(
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
		ID: workflowOperationRunID(req.OperationID), SessionID: sess.ID, ProjectID: sess.ProjectID,
		WorkflowID: manifest.ID, WorkflowVersion: manifest.Version, AttachPolicy: string(manifest.Attach.Policy),
		Status: api.WorkflowRunStatusRunning, CurrentPhase: manifest.FirstPhase(),
	}
	if workflowdef.SupportsBlueprints(manifest) {
		if m.BlueprintCreate == nil {
			return nil, ErrPlanDraftRequired
		}
		fixedPath := ""
		if manifest.Blueprint != nil {
			fixedPath = strings.TrimSpace(manifest.Blueprint.Path)
		}
		blueprintPath, err := m.resolvePlanForStart(ctx, req, sess.ProjectID, fixedPath, manifest.ID, sess.ID, slashText)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(blueprintPath) == "" {
			return nil, ErrPlanDraftRequired
		}
		run.BlueprintPath = blueprintPath
		workflowStartLog.Info("linked draft blueprint to workflow run",
			"session_id", sess.ID, "workflow_id", run.WorkflowID,
			"blueprint_path", blueprintPath, "blueprint_title", req.BlueprintTitle)
	}
	baselinePosture := m.baselinePostureForRun(ctx, m.rootRun(ctx, active), sess.Posture)
	vars := initializeRequestVars(nil, manifest.Request, req.Request, activationOnly)
	if !requestPending(vars) {
		vars, err = ApplyPhaseOnEnter(ctx, PhaseEnterRequest{
			Sessions: m.Sessions, SessionID: sess.ID, Manifest: manifest, PhaseID: run.CurrentPhase,
			Vars: vars, BlueprintPath: run.BlueprintPath, Registry: m.Registry,
			ReviewSpawnFilter: m.ReviewSpawnFilter,
		})
		if err != nil {
			return nil, err
		}
	}
	vars = ApplyMergedParams(vars, params)
	vars = ApplyAutoApproveEffects(vars, manifest)
	vars = StampDepthParamSkips(vars, manifest)
	vars = RecordWorkflowArtifactParams(vars, manifest)
	if manifest.Controls.ContentReview != nil {
		vars = ApplyPhaseContentReviewVars(vars, workflowdef.PhaseDef{ContentReview: manifest.Controls.ContentReview})
	}
	vars = saveBaselinePosture(vars, baselinePosture)
	if presetID != "" {
		vars = SetHostVar(vars, "workflow.preset_id", presetID)
	}
	if m.isSessionScopedWorkflow(ctx, sess.WorkspacePath, sess.ID, workflowID, version) {
		vars = SetHostVar(vars, "workflow_compose_summary_id", workflowdef.ManifestKey(workflowID, version))
	}
	terminalSink := false
	if def, ok := manifest.PhaseByID(run.CurrentPhase); ok {
		if !requestPending(vars) {
			vars, err = ApplyAutoApproveOnApprovePhase(ctx, m, run, manifest, def, vars)
			if err != nil {
				return nil, err
			}
			now := time.Now().UTC()
			terminalSink = completeTerminalPhaseEntry(run, def, now)
			if terminalSink {
				run.UpdatedAt = now
			}
		}
	}
	messages, startMessage := startBoundaryMessages(run, slashText, req.OperationID)
	if phaseID, feedback, pending := pendingPromptFromVars(vars); pending {
		var feedbackMessage *api.Message
		vars, feedbackMessage = buildFeedbackAnnouncement(run, phaseID, feedback, vars,
			workflowOperationMessageID(req.OperationID, "feedback:"+phaseID))
		if feedbackMessage != nil {
			messages = append(messages, *feedbackMessage)
		}
	}
	if terminalSink {
		boundary := newCommandBoundary(run, run.Revision, "completed", run.CurrentPhase, "")
		run.EndMessageID = boundary.ID
		messages = append(messages, boundary)
	}
	run.StartMessageID = api.SpanScrollAnchor(messages, startMessage)
	mutation := workflowStartMutation{ProjectDir: sess.WorkspacePath, Vars: vars, Messages: messages,
		Posture: workflowMutationPosture(run, vars, workflowStartPosture(manifest, run.CurrentPhase))}
	return &initializedWorkflowStart{Run: run, Vars: vars, Mutation: mutation}, nil
}

func (m *RunManager) activateStartedRun(
	ctx context.Context,
	replacementTarget, run *api.WorkflowRun,
	mutation workflowStartMutation,
	operationID, inputDigest string,
) error {
	replacedRuns, err := m.Store.ActivateStart(ctx, operationID, inputDigest, replacementTarget, run, mutation)
	if err != nil {
		return err
	}
	for i := range replacedRuns {
		m.afterRunCanceled(ctx, &replacedRuns[i], exitReasonSupersededByWorkflowStart)
	}
	return nil
}

func workflowStartPosture(manifest workflowdef.Manifest, phaseID string) api.SessionPosture {
	posture := strings.TrimSpace(manifest.InitialPosture)
	if phase, ok := manifest.PhaseByID(phaseID); ok {
		if phasePosture := strings.TrimSpace(phase.OnEnter.SetPosture); phasePosture != "" {
			posture = phasePosture
		}
	}
	return api.SessionPosture(posture)
}

func (m *RunManager) reviewedReplacementTarget(ctx context.Context, sessionID string, active *api.WorkflowRun, req api.StartWorkflowRunRequest, humanStart bool) (*api.WorkflowRun, error) {
	if active == nil {
		if strings.TrimSpace(req.ReplaceRunID) != "" || req.ExpectedRevision > 0 {
			return nil, fmt.Errorf("%w: session %s has no active workflow", ErrRunRevisionConflict, sessionID)
		}
		return nil, nil
	}
	if !humanStart {
		return active, nil
	}
	runID := strings.TrimSpace(req.ReplaceRunID)
	if runID == "" || req.ExpectedRevision < 1 {
		if m.IsAmbientRun(m.rootRun(ctx, active)) {
			return active, nil
		}
		return nil, ErrWorkflowReplacementTargetRequired
	}
	target, err := m.Store.Get(ctx, runID)
	if err != nil || target == nil || target.SessionID != sessionID || IsTerminal(target.Status) {
		return nil, fmt.Errorf("%w: reviewed replacement run %s is not active", ErrRunRevisionConflict, runID)
	}
	if target.Revision != req.ExpectedRevision {
		return nil, fmt.Errorf("%w: run %s expected revision %d, actual %d", ErrRunRevisionConflict, target.ID, req.ExpectedRevision, target.Revision)
	}
	return target, nil
}

func (m *RunManager) validateActiveRunForStart(active *api.WorkflowRun, activeAmbient bool, incoming workflowdef.Manifest, humanStart bool) error {
	if active == nil {
		return nil
	}
	if activeAmbient {
		if incoming.Attach.Policy == workflowdef.AttachPolicySessionCreate {
			return ErrActiveRunExists
		}
		return nil
	}
	if humanStart {
		return nil
	}
	return ErrActiveRunExists
}

func (m *RunManager) baselinePostureForRun(ctx context.Context, active *api.WorkflowRun, fallback api.SessionPosture) api.SessionPosture {
	if active == nil {
		return fallback
	}
	vars, err := m.Store.GetScaffoldVars(ctx, active.ID)
	if err != nil {
		return fallback
	}
	raw, _ := vars[hostVarBaselinePosture].(string)
	if session.ValidSessionPosture(raw) {
		return api.SessionPosture(raw)
	}
	return fallback
}
