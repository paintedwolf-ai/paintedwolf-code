package workflow

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session"
	workflowblueprintfiles "github.com/lycaon/lycaon/internal/workflow/blueprintfiles"
	"github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

type Approvals struct {
	Runs                    runstate.RunsRepository
	State                   runstate.StateRepository
	Records                 runstate.BlueprintsRepository
	Vars                    *runstate.Variables
	Resolver                *catalog.Resolver
	Sessions                session.Store
	Getter                  workflowblueprintfiles.Getter
	Registry                *conditions.ConditionRegistry
	Phases                  *workflowphases.Service
	OnHumanApprovalAdvanced HumanApprovalAdvancedHook
}

func (m *Approvals) ValidatePlanApprovalReady(ctx context.Context, projectID, blueprintPath, projectDir string) error {
	if m == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(blueprintPath) == "" {
		return nil
	}
	active, err := m.Runs.ActiveByProjectForBlueprint(ctx, projectID, blueprintPath)
	if err != nil || active == nil {
		return err
	}
	return m.validateHumanApprovalReady(ctx, active.ID, projectDir)
}

func (m *Approvals) SyncPlanApproved(ctx context.Context, projectID, blueprintPath, projectDir string) (*api.WorkflowRun, error) {
	if m == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(blueprintPath) == "" {
		return nil, nil
	}
	active, err := m.Runs.ActiveByProjectForBlueprint(ctx, projectID, blueprintPath)
	if err != nil || active == nil {
		return active, err
	}
	previousPhase := active.CurrentPhase
	previousStatus := active.Status
	run, err := m.SyncHumanApproval(ctx, active.ID, projectDir)
	if err != nil || run == nil {
		return run, err
	}
	if m.OnHumanApprovalAdvanced != nil &&
		!runstate.IsTerminal(run.Status) &&
		(previousPhase != run.CurrentPhase || previousStatus != run.Status) {
		m.OnHumanApprovalAdvanced(ctx, run)
	}
	return run, nil
}

func (m *Approvals) ApprovePlan(ctx context.Context, projectID, blueprintPath, projectDir, runID string, expectedRevision int64, expectedDigest string) (*api.WorkflowRun, error) {
	if m == nil || m.Getter == nil {
		return nil, fmt.Errorf("workflow blueprint approval not configured")
	}
	projectID = strings.TrimSpace(projectID)
	blueprintPath = strings.TrimSpace(blueprintPath)
	runID = strings.TrimSpace(runID)
	target, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if target == nil || target.ProjectID != projectID || strings.TrimSpace(target.BlueprintPath) != blueprintPath {
		return nil, runstate.ErrNoActiveRun
	}
	bp, err := m.Getter.Get(ctx, target.ProjectID, target.BlueprintPath)
	if err != nil {
		return nil, err
	}
	digest := workflowdef.HashBlueprintContent(bp.Content)
	if strings.TrimSpace(expectedDigest) == "" || digest != strings.TrimSpace(expectedDigest) {
		return nil, runstate.ErrBlueprintApprovalConflict
	}
	matched, err := m.Records.BlueprintApprovalMatches(ctx, target.ProjectID, target.BlueprintPath, target.ID, expectedRevision, digest)
	if err != nil {
		return nil, err
	}
	if matched {
		return target, nil
	}
	active, err := m.Runs.ActiveByProjectForBlueprint(ctx, projectID, blueprintPath)
	if err != nil {
		return nil, err
	}
	if active == nil || active.ID != runID {
		return nil, runstate.ErrNoActiveRun
	}
	if expectedRevision < 1 || active.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: run %s expected revision %d, actual %d", runstate.ErrRevisionConflict, active.ID, expectedRevision, active.Revision)
	}
	previousPhase, previousStatus := active.CurrentPhase, active.Status
	ctx = runstate.WithApprovalChannel(runstate.WithExpectedRevision(ctx, expectedRevision), runstate.ApprovalChannelAPI)
	run, err := m.SyncHumanApproval(ctx, active.ID, projectDir)
	if err != nil || run == nil {
		return run, err
	}
	if m.OnHumanApprovalAdvanced != nil && !runstate.IsTerminal(run.Status) &&
		(previousPhase != run.CurrentPhase || previousStatus != run.Status) {
		m.OnHumanApprovalAdvanced(ctx, run)
	}
	return run, nil
}

func (m *Approvals) SyncHumanApproval(ctx context.Context, runID, projectDir string) (*api.WorkflowRun, error) {
	if m == nil || strings.TrimSpace(runID) == "" {
		return nil, nil
	}
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return nil, err
	}
	def, ok := manifest.PhaseByID(run.CurrentPhase)
	if !ok || def.HumanApproval == nil {
		return run, nil
	}
	return m.recordHumanApproval(ctx, runID, projectDir, run.CurrentPhase, manifest, def.HumanApproval)
}

func (m *Approvals) validateHumanApprovalReady(ctx context.Context, runID, projectDir string) error {
	if m == nil || strings.TrimSpace(runID) == "" {
		return nil
	}
	ready := false
	bound := false
	if _, err := m.Vars.StampInProject(ctx, runID, projectDir, func(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
		ready, bound = false, false
		manifest, err := m.Resolver.ForRun(ctx, run)
		if err != nil {
			return nil, false, err
		}
		def, ok := manifest.PhaseByID(run.CurrentPhase)
		if !ok || def.HumanApproval == nil {
			return nil, false, nil
		}
		bound = true
		vars = workflowphases.RefreshHumanApprovalReady(ctx, m.Registry, def, vars, run.BlueprintPath, projectDir)
		ready = conditions.DotPathTruthy(vars, "human_approval.ready")
		return vars, true, nil
	}); err != nil {
		return err
	}
	if bound && !ready {
		return runstate.ErrHumanApprovalNotReady
	}
	return nil
}

func (m *Approvals) recordHumanApproval(ctx context.Context, runID, projectDir, phaseID string, manifest workflowdef.Manifest, cfg *workflowdef.HumanApprovalConfig) (*api.WorkflowRun, error) {
	if cfg == nil {
		return nil, nil
	}
	unlockVars := m.Vars.Lock(runID)
	varsUnlocked := false
	defer func() {
		if !varsUnlocked {
			unlockVars()
		}
	}()
	run, err := m.Runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if err := runstate.VerifyExpectedRevision(ctx, run); err != nil {
		return nil, err
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, runID)
	if err != nil {
		return nil, err
	}
	vars = workflowphases.RefreshHumanApprovalReady(ctx, m.Registry, workflowdef.PhaseDef{ID: phaseID, HumanApproval: cfg}, vars, run.BlueprintPath, projectDir)
	if !conditions.DotPathTruthy(vars, "human_approval.ready") {
		// Persist refreshed readiness without satisfying the gate.
		if err := m.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
			return nil, err
		}
		return run, runstate.ErrHumanApprovalNotReady
	}
	vars = runstate.SetHumanApprovalIssued(vars, true)
	content, err := workflowblueprintfiles.ResolveContent(ctx, run, projectDir, run.BlueprintPath)
	if err != nil {
		return nil, err
	}
	approvalDigest := workflowdef.HashBlueprintContent(content)
	vars = runstate.SetHumanApprovalHash(vars, approvalDigest)
	vars = runstate.SatisfyGateInVars(vars, "human_approval")
	channel := runstate.ApprovalChannel(ctx)
	if strings.TrimSpace(channel) == "" {
		channel = runstate.ApprovalChannelChat
	}
	if err := m.Records.CommitBlueprintApproval(ctx, run, projectDir, vars, approvalDigest, channel); err != nil {
		return nil, err
	}
	unlockVars()
	varsUnlocked = true
	return m.Phases.TryAutoAdvance(runstate.WithoutExpectedRevision(ctx), runID)
}

func (m *Approvals) InheritedMatches(
	ctx context.Context,
	parent *api.WorkflowRun,
	projectDir string,
	path string,
) (bool, error) {
	if m == nil || m.Runs == nil || parent == nil || strings.TrimSpace(path) == "" {
		return false, nil
	}
	vars, err := m.Runs.GetScaffoldVars(ctx, parent.ID)
	if err != nil {
		return false, err
	}
	hash, ok := runstate.DotPathString(vars, "human_approval.blueprint_hash")
	if !ok || !conditions.DotPathTruthy(vars, "human_approval.issued") {
		return false, nil
	}
	content, err := workflowblueprintfiles.ResolveContent(ctx, parent, projectDir, path)
	if err != nil {
		return false, err
	}
	return workflowdef.HumanApprovalContentMatches(hash, content), nil
}

func (m *Approvals) InheritedApprovalSnapshot(
	ctx context.Context,
	child *api.WorkflowRun,
) *inject.BlueprintApprovalView {
	if m == nil || child == nil || child.ParentRunID == nil {
		return nil
	}
	view := &inject.BlueprintApprovalView{
		Origin:      "inherited",
		ParentRunID: strings.TrimSpace(*child.ParentRunID),
		Status:      "invalid",
	}
	parent, err := m.Runs.Get(ctx, view.ParentRunID)
	if err != nil || parent == nil {
		return view
	}
	sess, err := m.Sessions.Get(ctx, child.SessionID)
	if err != nil || sess == nil {
		return view
	}
	approved, err := m.InheritedMatches(
		ctx,
		parent,
		sess.WorkspacePath,
		strings.TrimSpace(child.BlueprintPath),
	)
	if err == nil && approved {
		view.Status = "approved"
	}
	return view
}

func (m *Approvals) AwaitsHumanApproval(ctx context.Context, run *api.WorkflowRun, vars map[string]any) (bool, error) {
	// An absent approval wait needs no manifest lookup.
	if !scaffoldvars.HumanApprovalAwaiting(vars) {
		return false, nil
	}
	return m.currentPhaseHasHumanApproval(ctx, run)
}

func (m *Approvals) currentPhaseHasHumanApproval(ctx context.Context, run *api.WorkflowRun) (bool, error) {
	manifest, err := m.Resolver.ForRun(ctx, run)
	if err != nil {
		return false, err
	}
	phase, ok := manifest.PhaseByID(run.CurrentPhase)
	return ok && phase.HumanApproval != nil, nil
}

func (m *Approvals) AutoApproveOnPhase(ctx context.Context, run *api.WorkflowRun, manifest workflowdef.Manifest, def workflowdef.PhaseDef, vars map[string]any) (map[string]any, error) {
	if def.ID != "approve" || def.HumanApproval == nil || !runstate.ParameterIsTrue(vars, "auto_approve") {
		return vars, nil
	}
	projectDir := ""
	if m != nil && m.Sessions != nil && run != nil {
		if sess, err := m.Sessions.Get(ctx, run.SessionID); err == nil && sess != nil {
			projectDir = sess.WorkspacePath
		}
	}
	content, err := workflowblueprintfiles.ResolveContent(ctx, run, projectDir, def.HumanApproval.Blueprint)
	if err != nil {
		return nil, fmt.Errorf("auto_approve blueprint: %w", err)
	}
	vars = workflowphases.RefreshHumanApprovalReady(ctx, m.Registry, def, vars, run.BlueprintPath, projectDir)
	if !conditions.DotPathTruthy(vars, "human_approval.ready") {
		return vars, nil
	}
	vars = runstate.SetHumanApprovalIssued(vars, true)
	vars = runstate.SetHumanApprovalReady(vars, true)
	vars = runstate.SetHumanApprovalHash(vars, workflowdef.HashBlueprintContent(content))
	vars = runstate.SatisfyGateInVars(vars, "human_approval")
	return vars, nil
}

// HumanApprovalAdvancedHook reports executable work after an approval transition.
type HumanApprovalAdvancedHook func(ctx context.Context, run *api.WorkflowRun)
