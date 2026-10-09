package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrSubworkflowDepthExceeded is returned when a child run attempts to invoke another workflow.
var ErrSubworkflowDepthExceeded = errors.New("subworkflow depth exceeded (max 1)")

// ErrChildRunActive is returned when a parent already has a non-terminal child run.
var ErrChildRunActive = errors.New("child workflow run already active")

// InvokeChild starts a child workflow run for parentRunID and pauses the parent on the current phase.
func (m *RunManager) InvokeChild(ctx context.Context, parentRunID string, spec workflowdef.InvokeWorkflowSpec) (*api.WorkflowRun, error) {
	if m == nil || m.Store == nil {
		return nil, fmt.Errorf("workflow manager required")
	}
	parent, err := m.loadRun(ctx, parentRunID)
	if err != nil {
		return nil, err
	}
	if parent.ParentRunID != nil && strings.TrimSpace(*parent.ParentRunID) != "" {
		return nil, ErrSubworkflowDepthExceeded
	}
	workflowID := strings.TrimSpace(spec.WorkflowID)
	version := strings.TrimSpace(spec.Version)
	if workflowID == "" || version == "" {
		return nil, fmt.Errorf("invoke_workflow workflow_id and version required")
	}
	child, err := m.Store.LatestChildByParentRunID(ctx, parent.ID)
	if err != nil {
		return nil, err
	}
	if child != nil && !IsTerminal(child.Status) {
		if child.WorkflowID == workflowID && child.WorkflowVersion == version {
			return child, nil
		}
		return nil, ErrChildRunActive
	}
	if parent.Status != api.WorkflowRunStatusRunning {
		return nil, &NotRunnableError{RunID: parent.ID, Status: parent.Status, Reason: "parent not running"}
	}
	sess, err := m.Sessions.Get(ctx, parent.SessionID)
	if err != nil {
		return nil, err
	}
	childManifest, err := m.manifestForSession(ctx, sess.WorkspacePath, parent.SessionID, workflowID, version)
	if err != nil {
		return nil, err
	}
	if err := workflowdef.ManifestAllowsAsChild(childManifest); err != nil {
		return nil, err
	}
	blueprintPath, err := m.resolveChildBlueprint(ctx, parent, childManifest, spec.Blueprint, sess.WorkspacePath)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	parentID := parent.ID
	childRun := &api.WorkflowRun{
		ID:              uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("workflow-child:%s:%d:%s@%s", parent.ID, parent.Revision, childManifest.ID, childManifest.Version))).String(),
		SessionID:       parent.SessionID,
		ProjectID:       parent.ProjectID,
		ParentRunID:     &parentID,
		WorkflowID:      childManifest.ID,
		WorkflowVersion: childManifest.Version,
		AttachPolicy:    string(childManifest.Attach.Policy),
		Status:          api.WorkflowRunStatusRunning,
		CurrentPhase:    childManifest.FirstPhase(),
		BlueprintPath:   blueprintPath,
	}
	vars, err := ApplyPhaseOnEnter(ctx, PhaseEnterRequest{
		Sessions: m.Sessions, SessionID: parent.SessionID, Manifest: childManifest,
		PhaseID: childRun.CurrentPhase, BlueprintPath: childRun.BlueprintPath, Registry: m.Registry,
		ReviewSpawnFilter: m.ReviewSpawnFilter,
	})
	if err != nil {
		return nil, err
	}
	if childManifest.Controls.ContentReview != nil {
		vars = ApplyPhaseContentReviewVars(vars, workflowdef.PhaseDef{ContentReview: childManifest.Controls.ContentReview})
	}
	if childManifest.Request != nil {
		inherited := "Complete the work delegated by the parent workflow."
		if parentVars, varsErr := m.Store.GetScaffoldVars(ctx, parent.ID); varsErr == nil {
			if parentRequest, ok := requestStateFromVars(parentVars); ok && strings.TrimSpace(parentRequest.Text) != "" {
				inherited = parentRequest.Text
			}
		}
		vars = setRequestState(vars, childManifest.Request, requestStatusResolved, inherited, "inherited", 1, true)
	}
	vars = saveBaselinePosture(vars, sess.Posture)
	terminalSink := false
	if def, ok := childManifest.PhaseByID(childRun.CurrentPhase); ok {
		terminalSink = completeTerminalPhaseEntry(childRun, def, now)
		if terminalSink {
			childRun.UpdatedAt = now
		}
	}
	projectDir := sess.WorkspacePath
	parent.Status = api.WorkflowRunStatusPausedOnChild
	parent.UpdatedAt = now
	boundary := newCommandBoundary(parent, parent.Revision, "paused_on_child", parent.CurrentPhase, "")
	messages := []api.Message{boundary}
	if terminalSink {
		childBoundary := newCommandBoundary(childRun, childRun.Revision, "completed", childRun.CurrentPhase, "")
		childRun.EndMessageID = childBoundary.ID
		messages = append(messages, childBoundary)
	}
	mutation := workflowChildStartMutation{ProjectDir: projectDir, Vars: vars, Messages: messages,
		Posture: workflowMutationPosture(childRun, vars, workflowStartPosture(childManifest, childRun.CurrentPhase))}
	if err := m.Store.StartChild(ctx, parent, childRun, mutation); err != nil {
		return nil, err
	}
	if def, ok := childManifest.PhaseByID(childRun.CurrentPhase); ok && !terminalSink {
		m.triggerPhaseEnter(ctx, childRun, projectDir, def)
		if m.PhaseEnterHook != nil {
			m.PhaseEnterHook(ctx, &RunContext{
				SessionID:       childRun.SessionID,
				RunID:           childRun.ID,
				WorkflowID:      childRun.WorkflowID,
				WorkflowVersion: childRun.WorkflowVersion,
				Phase:           childRun.CurrentPhase,
			}, def)
		}
	}
	m.publish(ctx, sess, childRun)
	if terminalSink {
		if err := m.ReconcileTerminalRun(ctx, childRun); err != nil {
			return nil, err
		}
		return childRun, nil
	}
	m.publishSession(ctx, parent)
	return childRun, nil
}

func (m *RunManager) resolveChildBlueprint(
	ctx context.Context,
	parent *api.WorkflowRun,
	child workflowdef.Manifest,
	mode workflowdef.ChildBlueprintMode,
	projectDir string,
) (string, error) {
	switch mode {
	case workflowdef.ChildBlueprintNone:
		return "", nil
	case workflowdef.ChildBlueprintOwn:
		if m.BlueprintCreate == nil {
			return "", ErrPlanDraftRequired
		}
		fixedPath := ""
		if child.Blueprint != nil {
			fixedPath = strings.TrimSpace(child.Blueprint.Path)
		}
		return m.resolvePlanForStart(ctx, api.StartWorkflowRunRequest{}, parent.ProjectID, fixedPath, child.ID, parent.SessionID, "")
	case workflowdef.ChildBlueprintInherit:
		path := strings.TrimSpace(parent.BlueprintPath)
		if path == "" {
			return "", ErrInheritedBlueprintNotApproved
		}
		approved, err := m.inheritedBlueprintApprovalMatches(ctx, parent, projectDir, path)
		if err != nil {
			return "", err
		}
		if !approved {
			return "", ErrInheritedBlueprintNotApproved
		}
		return path, nil
	default:
		return "", fmt.Errorf("invoke_workflow.blueprint required")
	}
}

func (m *RunManager) inheritedBlueprintApprovalMatches(
	ctx context.Context,
	parent *api.WorkflowRun,
	projectDir string,
	path string,
) (bool, error) {
	if m == nil || m.Store == nil || parent == nil || strings.TrimSpace(path) == "" {
		return false, nil
	}
	vars, err := m.Store.GetScaffoldVars(ctx, parent.ID)
	if err != nil {
		return false, err
	}
	hash, ok := DotPathString(vars, "human_approval.blueprint_hash")
	if !ok || !conditions.DotPathTruthy(vars, "human_approval.issued") {
		return false, nil
	}
	content, err := ResolveBlueprintContent(ctx, m, parent, projectDir, path)
	if err != nil {
		return false, err
	}
	return workflowdef.HumanApprovalContentMatches(hash, content), nil
}

func (m *RunManager) maybeInvokeOnPhaseEnter(ctx context.Context, parent *api.WorkflowRun, def workflowdef.PhaseDef) error {
	if m == nil || parent == nil || def.InvokeWorkflow == nil {
		return nil
	}
	trigger := def.InvokeTrigger
	if trigger == "" {
		trigger = workflowdef.InvokeTriggerPhaseEnter
	}
	if trigger != workflowdef.InvokeTriggerPhaseEnter {
		return nil
	}
	if _, err := m.InvokeChild(ctx, parent.ID, *def.InvokeWorkflow); err != nil {
		return err
	}
	reloaded, err := m.loadRun(ctx, parent.ID)
	if err != nil {
		return err
	}
	parent.Status = reloaded.Status
	parent.UpdatedAt = reloaded.UpdatedAt
	return nil
}

// ReconcileTerminalRun resumes any parent and announces committed completion.
func (m *RunManager) ReconcileTerminalRun(ctx context.Context, child *api.WorkflowRun) error {
	if m == nil || child == nil {
		return nil
	}
	defer func() {
		if child.Status == api.WorkflowRunStatusComplete && m.OnRunCompleted != nil {
			m.OnRunCompleted(context.WithoutCancel(ctx), child)
		}
	}()
	if child.ParentRunID == nil {
		return nil
	}
	parentID := strings.TrimSpace(*child.ParentRunID)
	if parentID == "" {
		return nil
	}
	parent, err := m.loadRun(ctx, parentID)
	if err != nil {
		return err
	}
	resumingCompletedChild := parent.Status == api.WorkflowRunStatusRunning && child.Status == api.WorkflowRunStatusComplete
	if parent.Status != api.WorkflowRunStatusPausedOnChild && !resumingCompletedChild {
		return nil
	}
	if parent.Status == api.WorkflowRunStatusPausedOnChild {
		unlockVars := m.lockRunVars(parentID)
		vars, varsErr := m.Store.GetScaffoldVars(ctx, parentID)
		if varsErr != nil {
			unlockVars()
			return varsErr
		}
		vars = SetChildRunStatusVar(vars, string(child.Status))
		parent.Status = api.WorkflowRunStatusRunning
		parent.UpdatedAt = time.Now().UTC()
		boundary := newCommandBoundary(parent, parent.Revision, "resumed", parent.CurrentPhase, "child terminal")
		payload := struct{ ChildID, Status string }{child.ID, string(child.Status)}
		if err := m.commitCommand(ctx, parent, "resume_after_child", payload, vars, &boundary, "", workflowWorkerMutation{}, nil); err != nil {
			unlockVars()
			return err
		}
		unlockVars()
	}
	m.publishSession(ctx, parent)
	if child.Status == api.WorkflowRunStatusComplete {
		for i := 0; i < 3; i++ {
			parent, err = m.loadRun(ctx, parentID)
			if err != nil {
				return err
			}
			if IsTerminal(parent.Status) {
				return nil
			}
			if _, err := m.TryAutoAdvance(ctx, parentID); err != nil {
				return err
			}
		}
	}
	return nil
}

// RecoverTerminalChildren closes the commit window between a child becoming
// terminal and the corresponding paused parent being resumed.
func (m *RunManager) RecoverTerminalChildren(ctx context.Context) error {
	parents, err := m.Store.ListPausedOnChild(ctx)
	if err != nil {
		return err
	}
	for i := range parents {
		child, childErr := m.Store.LatestChildByParentRunID(ctx, parents[i].ID)
		if childErr != nil {
			return childErr
		}
		if child == nil || !IsTerminal(child.Status) {
			continue
		}
		if handleErr := m.ReconcileTerminalRun(ctx, child); handleErr != nil {
			return handleErr
		}
	}
	return nil
}

func (m *RunManager) resumeParentAfterChildExit(ctx context.Context, child *api.WorkflowRun, childStatus string) (*api.WorkflowRun, error) {
	if child == nil || child.ParentRunID == nil {
		return nil, nil
	}
	parentID := strings.TrimSpace(*child.ParentRunID)
	// The parent keeps running while the child does; stamping the child's exit
	// status is a read/mutate/write on the parent's vars like any other.
	unlockVars := m.lockRunVars(parentID)
	defer unlockVars()
	parent, err := m.loadRun(ctx, parentID)
	if err != nil {
		return nil, err
	}
	vars, err := m.Store.GetScaffoldVars(ctx, parentID)
	if err != nil {
		return nil, err
	}
	vars = SetChildRunStatusVar(vars, strings.TrimSpace(childStatus))
	if parent.Status == api.WorkflowRunStatusPausedOnChild {
		parent.Status = api.WorkflowRunStatusRunning
		parent.UpdatedAt = time.Now().UTC()
		boundary := newCommandBoundary(parent, parent.Revision, "resumed", parent.CurrentPhase, "child exit")
		payload := struct{ ChildID, Status string }{child.ID, strings.TrimSpace(childStatus)}
		if err := m.commitCommand(ctx, parent, "resume_after_child_exit", payload, vars, &boundary, "", workflowWorkerMutation{}, nil); err != nil {
			return nil, err
		}
		m.publishSession(ctx, parent)
	}
	return parent, nil
}
