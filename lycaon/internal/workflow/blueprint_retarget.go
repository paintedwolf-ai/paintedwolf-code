package workflow

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/pkg/api"
)

// maybeRetargetBoundBlueprint moves a provisional bound file once a title is declared.
func (m *RunManager) maybeRetargetBoundBlueprint(ctx context.Context, run *api.WorkflowRun) *api.WorkflowRun {
	if m == nil || run == nil || m.BlueprintCreate == nil || m.BlueprintGet == nil {
		return nil
	}
	from := filepath.ToSlash(strings.TrimSpace(run.BlueprintPath))
	if from == "" || !blueprint.IsProvisionalPath(from) {
		return nil
	}
	bp, err := m.BlueprintGet.Get(ctx, run.ProjectID, from)
	if err != nil || bp == nil {
		return nil
	}
	title, ok := blueprintfile.DeclaredTitle(bp.Content)
	if !ok {
		return nil
	}
	to, err := m.BlueprintCreate.RetargetToTitle(ctx, run.ProjectID, from, title)
	if err != nil {
		return nil
	}
	to = filepath.ToSlash(strings.TrimSpace(to))
	if to == "" || to == from {
		return nil
	}
	next, err := m.Store.Get(ctx, run.ID)
	if err != nil || next == nil {
		run.BlueprintPath = to
		return run
	}
	return next
}

// RebindBlueprintPath rewrites every host binding that still names from.
func (m *RunManager) RebindBlueprintPath(ctx context.Context, projectID, from, to string) error {
	if m == nil || m.Store == nil {
		return nil
	}
	from = filepath.ToSlash(strings.TrimSpace(from))
	to = filepath.ToSlash(strings.TrimSpace(to))
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || from == "" || to == "" || from == to {
		return nil
	}
	sessions, err := m.Store.RelocateBlueprintPath(ctx, projectID, from, to)
	if err != nil {
		return err
	}
	for _, sessionID := range sessions {
		_ = m.retargetBlueprintTranscriptPath(ctx, sessionID, from, to)
		m.rebindPendingLaunchPath(ctx, sessionID, from, to)
	}
	if run, err := m.Store.ActiveByProjectForBlueprint(ctx, projectID, to); err == nil && run != nil {
		stamped, _ := m.StampRunVars(ctx, run.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
			return SetHumanApprovalBlueprintPath(vars, to), true, nil
		})
		if stamped != nil {
			run = stamped
		}
		m.publish(ctx, nil, run)
	}
	return nil
}

func (m *RunManager) retargetBlueprintTranscriptPath(ctx context.Context, sessionID, from, to string) error {
	if m == nil || m.Sessions == nil {
		return nil
	}
	msgs, err := m.Sessions.GetMessages(ctx, sessionID)
	if err != nil {
		return err
	}
	existing, ok := findBlueprintTranscriptMessage(msgs, from)
	if !ok || existing.Blueprint == nil {
		return nil
	}
	patch := existing
	meta := *existing.Blueprint
	meta.BlueprintPath = to
	patch.Blueprint = &meta
	if _, err := m.Sessions.UpdateMessage(ctx, sessionID, existing.ID, patch); err != nil {
		return err
	}
	m.publishMessagePatch(ctx, sessionID, patch)
	return nil
}

func (m *RunManager) rebindPendingLaunchPath(ctx context.Context, sessionID, from, to string) {
	if m == nil || m.SessionScaffold == nil {
		return
	}
	vars, err := m.SessionScaffold.GetVars(ctx, sessionID)
	if err != nil {
		return
	}
	if pendingBlueprintLaunchPath(vars) != from {
		return
	}
	_ = m.SessionScaffold.UpsertVars(ctx, sessionID, setPendingBlueprintLaunchPath(vars, to))
}
