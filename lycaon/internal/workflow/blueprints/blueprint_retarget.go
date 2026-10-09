package blueprints

import (
	"context"
	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"path/filepath"
	"strings"
)

// maybeRetargetBoundBlueprint moves a provisional bound file once a title is declared.
func (m *Service) maybeRetargetBoundBlueprint(ctx context.Context, run *api.WorkflowRun) *api.WorkflowRun {
	if m == nil || run == nil || m.Creator == nil || m.Getter == nil {
		return nil
	}
	from := filepath.ToSlash(strings.TrimSpace(run.BlueprintPath))
	if from == "" || !blueprint.IsProvisionalPath(from) {
		return nil
	}
	bp, err := m.Getter.Get(ctx, run.ProjectID, from)
	if err != nil || bp == nil {
		return nil
	}
	title, ok := blueprintfile.DeclaredTitle(bp.Content)
	if !ok {
		return nil
	}
	to, err := m.Creator.RetargetToTitle(ctx, run.ProjectID, from, title)
	if err != nil {
		return nil
	}
	to = filepath.ToSlash(strings.TrimSpace(to))
	if to == "" || to == from {
		return nil
	}
	next, err := m.Runs.Get(ctx, run.ID)
	if err != nil || next == nil {
		run.BlueprintPath = to
		return run
	}
	return next
}

// RebindBlueprintPath rewrites every host binding that still names from.
func (m *Service) RebindBlueprintPath(ctx context.Context, projectID, from, to string) error {
	if m == nil || m.Runs == nil {
		return nil
	}
	from = filepath.ToSlash(strings.TrimSpace(from))
	to = filepath.ToSlash(strings.TrimSpace(to))
	projectID = strings.TrimSpace(projectID)
	if projectID == "" || from == "" || to == "" || from == to {
		return nil
	}
	sessions, err := m.Bindings.RelocateBlueprintPath(ctx, projectID, from, to)
	if err != nil {
		return err
	}
	for _, sessionID := range sessions {
		_ = m.retargetBlueprintTranscriptPath(ctx, sessionID, from, to)
		m.Scaffold.RebindPendingLaunchPath(ctx, sessionID, from, to)
	}
	if run, err := m.Runs.ActiveByProjectForBlueprint(ctx, projectID, to); err == nil && run != nil {
		stamped, _ := m.Vars.Stamp(ctx, run.ID, func(_ context.Context, _ *api.WorkflowRun, vars map[string]any) (map[string]any, bool, error) {
			return runstate.SetHumanApprovalBlueprintPath(vars, to), true, nil
		})
		if stamped != nil {
			run = stamped
		}
		m.Publication.Publish(ctx, nil, run)
	}
	return nil
}

func (m *Service) retargetBlueprintTranscriptPath(ctx context.Context, sessionID, from, to string) error {
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
	m.Transcript.PublishPatch(ctx, sessionID, patch)
	return nil
}
