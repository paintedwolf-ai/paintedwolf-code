package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *RunManager) resolvePlanForStart(ctx context.Context, req api.StartWorkflowRunRequest, projectID, fixedPath, sourceWorkflowID, sessionID, slashText string) (string, error) {
	if m == nil || m.BlueprintCreate == nil {
		return "", nil
	}
	titleSource := blueprint.SlugTitle(strings.TrimSpace(req.Request))
	if titleSource == "" {
		titleSource = blueprintTitleFromSlashText(slashText)
	}
	if titleSource == "" {
		titleSource = m.blueprintTitleFromSession(ctx, sessionID)
	}
	title := mintBlueprintTitle(req.BlueprintTitle, titleSource)
	if blueprintPath := strings.TrimSpace(req.BlueprintPath); blueprintPath != "" {
		if err := m.BlueprintCreate.ResolveDraft(ctx, projectID, blueprintPath); err != nil {
			if errors.Is(err, blueprint.ErrNotFound) {
				return "", ErrPlanNotFound
			}
			if errors.Is(err, blueprint.ErrInvalidStatus) {
				return "", ErrPlanNotDraft
			}
			return "", err
		}
		return m.retargetDraft(ctx, projectID, blueprintPath, title)
	}
	if pending := m.takePendingBlueprintLaunchPath(ctx, sessionID); pending != "" {
		if err := m.BlueprintCreate.ResolveDraft(ctx, projectID, pending); err != nil {
			if errors.Is(err, blueprint.ErrNotFound) {
				return "", ErrPlanNotFound
			}
			if errors.Is(err, blueprint.ErrInvalidStatus) {
				return "", ErrPlanNotDraft
			}
			return "", err
		}
		return m.retargetDraft(ctx, projectID, pending, title)
	}
	createPath := strings.TrimSpace(fixedPath) // options keeps a fixed shared file; plan omits → mint
	path, err := m.BlueprintCreate.CreateBlueprint(ctx, projectID, title, createPath, sourceWorkflowID)
	if err != nil {
		return "", err
	}
	return m.retargetDraft(ctx, projectID, path, title)
}

func blueprintTitleFromSlashText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	firstSpace := strings.IndexAny(text, " \t\r\n")
	if firstSpace < 0 {
		return ""
	}
	return blueprint.SlugTitle(strings.TrimSpace(text[firstSpace+1:]))
}

func (m *RunManager) retargetDraft(ctx context.Context, projectID, path, title string) (string, error) {
	if m == nil || m.BlueprintCreate == nil || !blueprint.IsProvisionalPath(path) || blueprintfile.IsPlaceholderTitle(title) {
		return path, nil
	}
	moved, err := m.BlueprintCreate.RetargetToTitle(ctx, projectID, path, title)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(moved) == "" {
		return path, nil
	}
	return moved, nil
}

func mintBlueprintTitle(planName, userIntentSlug string) string {
	title := strings.TrimSpace(planName)
	if !blueprintfile.IsPlaceholderTitle(title) {
		return title
	}
	if slug := strings.TrimSpace(userIntentSlug); slug != "" {
		return slug
	}
	return blueprintfile.PlaceholderTitle
}

func (m *RunManager) blueprintTitleFromSession(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || m.Sessions == nil || sessionID == "" {
		return ""
	}
	msgs, err := m.Sessions.GetMessages(ctx, sessionID)
	if err != nil || len(msgs) == 0 {
		return ""
	}
	boundary := api.UserIntentBoundary(msgs)
	if boundary <= 0 || boundary > len(msgs) {
		return ""
	}
	msg := msgs[boundary-1]
	return blueprint.SlugTitle(msg.Content)
}

// ValidatePlanApprovalReady refreshes readiness without satisfying the gate.
func (m *RunManager) ValidatePlanApprovalReady(ctx context.Context, projectID, blueprintPath, projectDir string) error {
	if m == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(blueprintPath) == "" {
		return nil
	}
	active, err := m.Store.ActiveByProjectForBlueprint(ctx, projectID, blueprintPath)
	if err != nil || active == nil {
		return err
	}
	return m.validateHumanApprovalReady(ctx, active.ID, projectDir)
}

// SyncPlanApproved records approval after the blueprint is durable.
func (m *RunManager) SyncPlanApproved(ctx context.Context, projectID, blueprintPath, projectDir string) (*api.WorkflowRun, error) {
	if m == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(blueprintPath) == "" {
		return nil, nil
	}
	active, err := m.Store.ActiveByProjectForBlueprint(ctx, projectID, blueprintPath)
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
		!IsTerminal(run.Status) &&
		(previousPhase != run.CurrentPhase || previousStatus != run.Status) {
		m.OnHumanApprovalAdvanced(ctx, run)
	}
	return run, nil
}

// ApprovePlan binds approval to reviewed bytes and revision.
func (m *RunManager) ApprovePlan(ctx context.Context, projectID, blueprintPath, projectDir, runID string, expectedRevision int64, expectedDigest string) (*api.WorkflowRun, error) {
	if m == nil || m.BlueprintGet == nil {
		return nil, fmt.Errorf("workflow blueprint approval not configured")
	}
	projectID = strings.TrimSpace(projectID)
	blueprintPath = strings.TrimSpace(blueprintPath)
	runID = strings.TrimSpace(runID)
	target, err := m.Store.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if target == nil || target.ProjectID != projectID || strings.TrimSpace(target.BlueprintPath) != blueprintPath {
		return nil, ErrNoActiveRun
	}
	bp, err := m.BlueprintGet.Get(ctx, target.ProjectID, target.BlueprintPath)
	if err != nil {
		return nil, err
	}
	digest := workflowdef.HashBlueprintContent(bp.Content)
	if strings.TrimSpace(expectedDigest) == "" || digest != strings.TrimSpace(expectedDigest) {
		return nil, ErrBlueprintApprovalConflict
	}
	matched, err := m.Store.BlueprintApprovalMatches(ctx, target.ProjectID, target.BlueprintPath, target.ID, expectedRevision, digest)
	if err != nil {
		return nil, err
	}
	if matched {
		return target, nil
	}
	active, err := m.Store.ActiveByProjectForBlueprint(ctx, projectID, blueprintPath)
	if err != nil {
		return nil, err
	}
	if active == nil || active.ID != runID {
		return nil, ErrNoActiveRun
	}
	if expectedRevision < 1 || active.Revision != expectedRevision {
		return nil, fmt.Errorf("%w: run %s expected revision %d, actual %d", ErrRunRevisionConflict, active.ID, expectedRevision, active.Revision)
	}
	previousPhase, previousStatus := active.CurrentPhase, active.Status
	ctx = WithApprovalChannel(WithExpectedRevision(ctx, expectedRevision), ApprovalChannelAPI)
	run, err := m.SyncHumanApproval(ctx, active.ID, projectDir)
	if err != nil || run == nil {
		return run, err
	}
	if m.OnHumanApprovalAdvanced != nil && !IsTerminal(run.Status) &&
		(previousPhase != run.CurrentPhase || previousStatus != run.Status) {
		m.OnHumanApprovalAdvanced(ctx, run)
	}
	return run, nil
}
