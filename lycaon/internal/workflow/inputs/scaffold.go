package inputs

import (
	"context"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// ValidateUserFacingStart allows catalog-tier bundled workflows or composed/persisted overlays.

// SessionScaffoldStore persists per-session workflow proposal state.

// NoteWorkflowStartProposal records the catalog workflow the coordinator recommended.
func (m *Scaffold) NoteWorkflowStartProposal(ctx context.Context, sessionID, workflowID, version string) error {
	if m == nil || m.Store == nil {
		return nil
	}
	workflowID = strings.TrimSpace(workflowID)
	version = strings.TrimSpace(version)
	if workflowID == "" || version == "" {
		return workflowdef.ErrUnknownWorkflow
	}
	vars, err := m.Store.GetVars(ctx, sessionID)
	if err != nil {
		return err
	}
	vars = runstate.SetWorkflowStartProposal(vars, workflowID, version, "")
	return m.Store.UpsertVars(ctx, sessionID, vars)
}

// SetPendingBlueprintLaunchPath stores a seeded blueprint path for the next human start
// (Den arm → composer send). Cleared on start or exit.
func (m *Scaffold) SetPendingBlueprintLaunchPath(ctx context.Context, sessionID, path string) error {
	if m == nil || m.Store == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	path = strings.TrimSpace(path)
	if sessionID == "" || path == "" {
		return nil
	}
	vars, err := m.Store.GetVars(ctx, sessionID)
	if err != nil {
		return err
	}
	vars = runstate.SetPendingBlueprintLaunchPath(vars, path)
	return m.Store.UpsertVars(ctx, sessionID, vars)
}

// TakePendingBlueprintLaunchPath returns and clears a deferred launch seed path.
func (m *Scaffold) TakePendingBlueprintLaunchPath(ctx context.Context, sessionID string) string {
	if m == nil || m.Store == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	vars, err := m.Store.GetVars(ctx, sessionID)
	if err != nil {
		return ""
	}
	path := runstate.PendingBlueprintLaunchPath(vars)
	if path == "" {
		return ""
	}
	vars = runstate.SetPendingBlueprintLaunchPath(vars, "")
	_ = m.Store.UpsertVars(ctx, sessionID, vars)
	return path
}

func (m *Scaffold) ClearStartState(ctx context.Context, sessionID string) {
	if m == nil || m.Store == nil {
		return
	}
	vars, err := m.Store.GetVars(ctx, sessionID)
	if err != nil {
		return
	}
	vars = runstate.ClearWorkflowStartState(vars)
	_ = m.Store.UpsertVars(ctx, sessionID, vars)
}

type Scaffold struct{ Store runstate.ScaffoldRepository }

func (m *Scaffold) RebindPendingLaunchPath(ctx context.Context, sessionID, from, to string) {
	if m == nil || m.Store == nil {
		return
	}
	vars, err := m.Store.GetVars(ctx, sessionID)
	if err != nil {
		return
	}
	if runstate.PendingBlueprintLaunchPath(vars) != from {
		return
	}
	_ = m.Store.UpsertVars(ctx, sessionID, runstate.SetPendingBlueprintLaunchPath(vars, to))
}

// ProposedStart reads the pending catalog selection from the session scaffold.
func (m *Scaffold) ProposedStart(ctx context.Context, sessionID string) (workflowID, version, presetID string, ok bool, err error) {
	if m == nil || m.Store == nil {
		return
	}
	vars, err := m.Store.GetVars(ctx, sessionID)
	if err != nil {
		return "", "", "", false, err
	}
	workflowID, version, ok = runstate.ProposedWorkflow(vars)
	return workflowID, version, runstate.ProposedPresetID(vars), ok, nil
}
