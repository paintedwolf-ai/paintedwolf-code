package blueprints

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/pkg/api"
)

// AfterWrite implements native.BlueprintWriteObserver.
func (m *Service) AfterWrite(ctx context.Context, sessionID, relPath string) {
	m.NotifyBlueprintPathWritten(ctx, sessionID, "", relPath)
}

// NotifyBlueprintPathWritten advances the active run when its bound blueprint file
// was written.
func (m *Service) NotifyBlueprintPathWritten(ctx context.Context, sessionID, projectID, relPath string) {
	if m == nil {
		return
	}
	relPath = filepath.ToSlash(strings.TrimSpace(relPath))
	if relPath == "" {
		return
	}
	var (
		active *api.WorkflowRun
		err    error
	)
	sessionID = strings.TrimSpace(sessionID)
	if sessionID != "" {
		active, err = m.Runs.ActiveBySession(ctx, sessionID)
	} else {
		active, err = m.Runs.ActiveByProjectForBlueprint(ctx, projectID, relPath)
	}
	if err != nil || active == nil {
		return
	}
	bound := filepath.ToSlash(strings.TrimSpace(active.BlueprintPath))
	if bound == "" || bound != relPath {
		return
	}
	if moved := m.maybeRetargetBoundBlueprint(ctx, active); moved != nil {
		active = moved
	}
	changedWarning := false
	if vars, err := m.Runs.GetScaffoldVars(ctx, active.ID); err == nil {
		changedWarning = scaffoldvars.HumanApprovalAwaiting(vars)
	}
	_ = m.SyncTranscriptForRun(ctx, active, changedWarning)
	_, _ = m.Phases.TryAutoAdvanceThroughCommittedGates(ctx, active.ID, 8)
}
