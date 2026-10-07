package workflow

import (
	"context"
	"strings"

	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ValidateUserFacingStart allows catalog-tier bundled workflows or composed/persisted overlays.
func (m *RunManager) ValidateUserFacingStart(ctx context.Context, projectDir, sessionID, workflowID, version string) error {
	if m == nil {
		return workflowdef.ErrUnknownWorkflow
	}
	if m.Manifests != nil && m.Manifests.CatalogStartable(workflowID, version) {
		return nil
	}
	registry, scopes, err := m.Resolver.Resolve(ctx, projectDir, sessionID)
	if err != nil {
		return workflowdef.ErrUnknownWorkflow
	}
	manifest, err := registry.Get(workflowID, version)
	if err != nil || manifest.Retired {
		return workflowdef.ErrUnknownWorkflow
	}
	switch scopes[workflowdef.ManifestKey(workflowID, version)] {
	case string(api.WorkflowScopeSession), string(api.WorkflowScopeProject):
		return nil
	default:
		return workflowdef.ErrUnknownWorkflow
	}
}

// FilterProductCatalogSummaries keeps only catalog-visible bundled entries.
func FilterProductCatalogSummaries(rows []api.WorkflowSummary, manifests map[string]workflowdef.Manifest) []api.WorkflowSummary {
	if len(rows) == 0 {
		return rows
	}
	out := make([]api.WorkflowSummary, 0, len(rows))
	for _, row := range rows {
		if manifest, ok := manifests[workflowdef.ManifestKey(row.ID, row.Version)]; ok && manifest.Retired {
			continue
		}
		if row.Scope != api.WorkflowScopeBundled {
			out = append(out, row)
			continue
		}
		if m, ok := manifests[workflowdef.ManifestKey(row.ID, row.Version)]; ok && m.IsCatalogVisible() {
			out = append(out, row)
		}
	}
	return out
}

const (
	sessionVarWorkflowStartProposed      = "workflow_start_proposed"
	sessionVarPendingBlueprintLaunchPath = "pending_blueprint_launch_path"
)

// SessionScaffoldStore persists per-session workflow proposal state.
type SessionScaffoldStore interface {
	GetVars(ctx context.Context, sessionID string) (map[string]any, error)
	UpsertVars(ctx context.Context, sessionID string, vars map[string]any) error
}

func clearWorkflowStartState(vars map[string]any) map[string]any {
	vars = cloneVars(vars)
	delete(vars, sessionVarWorkflowStartProposed)
	delete(vars, sessionVarPendingBlueprintLaunchPath)
	return vars
}

func setPendingBlueprintLaunchPath(vars map[string]any, path string) map[string]any {
	vars = cloneVars(vars)
	path = strings.TrimSpace(path)
	if path == "" {
		delete(vars, sessionVarPendingBlueprintLaunchPath)
		return vars
	}
	vars[sessionVarPendingBlueprintLaunchPath] = path
	return vars
}

func pendingBlueprintLaunchPath(vars map[string]any) string {
	if vars == nil {
		return ""
	}
	path, _ := vars[sessionVarPendingBlueprintLaunchPath].(string)
	return strings.TrimSpace(path)
}

func setWorkflowStartProposal(vars map[string]any, workflowID, version, presetID string) map[string]any {
	vars = cloneVars(vars)
	raw := map[string]any{
		"workflow_id":      strings.TrimSpace(workflowID),
		"workflow_version": strings.TrimSpace(version),
	}
	if strings.TrimSpace(presetID) != "" {
		raw["preset_id"] = strings.TrimSpace(presetID)
	}
	vars[sessionVarWorkflowStartProposed] = raw
	return vars
}

func proposedWorkflow(vars map[string]any) (workflowID, version string, ok bool) {
	if vars == nil {
		return "", "", false
	}
	raw, _ := vars[sessionVarWorkflowStartProposed].(map[string]any)
	if raw == nil {
		return "", "", false
	}
	workflowID, _ = raw["workflow_id"].(string)
	version, _ = raw["workflow_version"].(string)
	workflowID = strings.TrimSpace(workflowID)
	version = strings.TrimSpace(version)
	if workflowID == "" || version == "" {
		return "", "", false
	}
	return workflowID, version, true
}

// NoteWorkflowStartProposal records the catalog workflow the coordinator recommended.
func (m *RunManager) NoteWorkflowStartProposal(ctx context.Context, sessionID, workflowID, version string) error {
	if m == nil || m.SessionScaffold == nil {
		return nil
	}
	workflowID = strings.TrimSpace(workflowID)
	version = strings.TrimSpace(version)
	if workflowID == "" || version == "" {
		return workflowdef.ErrUnknownWorkflow
	}
	vars, err := m.SessionScaffold.GetVars(ctx, sessionID)
	if err != nil {
		return err
	}
	vars = setWorkflowStartProposal(vars, workflowID, version, "")
	return m.SessionScaffold.UpsertVars(ctx, sessionID, vars)
}

// SetPendingBlueprintLaunchPath stores a seeded blueprint path for the next human start
// (Den arm → composer send). Cleared on start or exit.
func (m *RunManager) SetPendingBlueprintLaunchPath(ctx context.Context, sessionID, path string) error {
	if m == nil || m.SessionScaffold == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	path = strings.TrimSpace(path)
	if sessionID == "" || path == "" {
		return nil
	}
	vars, err := m.SessionScaffold.GetVars(ctx, sessionID)
	if err != nil {
		return err
	}
	vars = setPendingBlueprintLaunchPath(vars, path)
	return m.SessionScaffold.UpsertVars(ctx, sessionID, vars)
}

// takePendingBlueprintLaunchPath returns and clears a deferred launch seed path.
func (m *RunManager) takePendingBlueprintLaunchPath(ctx context.Context, sessionID string) string {
	if m == nil || m.SessionScaffold == nil {
		return ""
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	vars, err := m.SessionScaffold.GetVars(ctx, sessionID)
	if err != nil {
		return ""
	}
	path := pendingBlueprintLaunchPath(vars)
	if path == "" {
		return ""
	}
	vars = setPendingBlueprintLaunchPath(vars, "")
	_ = m.SessionScaffold.UpsertVars(ctx, sessionID, vars)
	return path
}

func (m *RunManager) clearSessionWorkflowStartState(ctx context.Context, sessionID string) {
	if m == nil || m.SessionScaffold == nil {
		return
	}
	vars, err := m.SessionScaffold.GetVars(ctx, sessionID)
	if err != nil {
		return
	}
	vars = clearWorkflowStartState(vars)
	_ = m.SessionScaffold.UpsertVars(ctx, sessionID, vars)
}
