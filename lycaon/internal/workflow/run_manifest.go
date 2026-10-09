package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/lycaon/lycaon/internal/extpacks"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

// ScaffoldVarsForSession returns scaffold vars for the active workflow run, if any.
func (m *RunManager) ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error) {
	if m == nil {
		return nil, nil
	}
	active, err := m.Store.ActiveBySession(ctx, sessionID)
	if err != nil || active == nil {
		return nil, err
	}
	return m.Store.GetScaffoldVars(ctx, active.ID)
}

func (m *RunManager) registryFor(ctx context.Context, projectDir, sessionID string) (*workflowdef.Registry, error) {
	var base *workflowdef.Registry
	var err error
	if m != nil {
		base, _, err = m.Resolver.Resolve(ctx, projectDir, sessionID)
		if err != nil {
			return nil, err
		}
	} else {
		base = workflowdef.NewRegistry(nil)
	}
	if m == nil || m.Manifests == nil {
		return base, nil
	}
	overlay := m.Manifests.All()
	if len(overlay) == 0 {
		return base, nil
	}
	// Merge into a fresh registry because both inputs may have concurrent readers.
	baseEntries := base.All()
	merged := make(map[string]workflowdef.Manifest, len(baseEntries)+len(overlay))
	for k, v := range baseEntries {
		merged[k] = v
	}
	for k, v := range overlay {
		merged[k] = v
	}
	return workflowdef.NewRegistry(merged), nil
}

func (m *RunManager) manifestForSession(ctx context.Context, projectDir, sessionID, workflowID, version string) (workflowdef.Manifest, error) {
	reg, err := m.registryFor(ctx, projectDir, sessionID)
	if err != nil {
		return workflowdef.Manifest{}, err
	}
	return reg.Get(workflowID, version)
}

func (m *RunManager) isSessionScopedWorkflow(ctx context.Context, projectDir, sessionID, workflowID, version string) bool {
	if m == nil {
		return false
	}
	_, scopes, err := m.Resolver.Resolve(ctx, projectDir, sessionID)
	if err != nil {
		return false
	}
	return scopes[workflowdef.ManifestKey(workflowID, version)] == string(api.WorkflowScopeSession)
}

func (m *RunManager) manifestForRun(ctx context.Context, run *api.WorkflowRun) (workflowdef.Manifest, error) {
	if run == nil {
		return workflowdef.Manifest{}, fmt.Errorf("workflow run required")
	}
	manifest, err := m.manifestForSession(ctx, m.projectDirForRun(ctx, run), run.SessionID, run.WorkflowID, run.WorkflowVersion)
	if errors.Is(err, workflowdef.ErrUnknownWorkflow) {
		return workflowdef.Manifest{}, &WorkflowVersionUnavailableError{WorkflowID: run.WorkflowID, Version: run.WorkflowVersion}
	}
	return manifest, err
}

// runArchive names the sealed version a retired run reads its guidance from.
func runArchive(manifest workflowdef.Manifest) string {
	if !manifest.Retired {
		return ""
	}
	return extpacks.ArchiveKey(manifest.ID, manifest.Version)
}

func (m *RunManager) projectDirForRun(ctx context.Context, run *api.WorkflowRun) string {
	if m == nil || run == nil {
		return ""
	}
	return m.projectDir(ctx, run.SessionID)
}

func (m *RunManager) triggerPhaseEnter(ctx context.Context, run *api.WorkflowRun, projectDir string, def workflowdef.PhaseDef) {
	if m == nil {
		return
	}
	if def.HumanApproval != nil && runHasBlueprint(run) {
		if err := m.syncBlueprintTranscriptForRun(ctx, run, false); err != nil {
			slog.WarnContext(ctx, "sync blueprint approval transcript", "run_id", run.ID, "phase", def.ID, "error", err)
		}
	}
	m.triggerObligationsOnEnter(ctx, run, projectDir, def)
	m.appendPhaseExplain(ctx, run, def)
	if def.ReviewLoop != nil {
		if err := m.stampReviewLoopEvidence(ctx, run); err != nil {
			slog.WarnContext(ctx, "stamp review loop evidence", "run_id", run.ID, "error", err)
		}
	}
}
