package workflowadmin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/orchestration"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Handler) StartOrchestratedTopologyForRun(ctx context.Context, sessionID string, run *wire.WorkflowRun) {
	if s == nil || s.Orchestrator == nil || run == nil {
		return
	}
	// Workflow bytes come from the resolved catalog.
	catalog, err := extpacks.CatalogForConsumers()
	if err != nil {
		return
	}
	manifest, err := orchestration.LoadWorkflowManifest(catalog, run.WorkflowID, run.WorkflowVersion)
	if err != nil || strings.TrimSpace(manifest.TopologyID) == "" {
		return
	}
	// Start only after the bound phase becomes active.
	if !manifest.BoundPhases[strings.TrimSpace(run.CurrentPhase)] {
		return
	}
	sess, err := s.Store.Get(ctx, sessionID)
	if err != nil || sess == nil {
		return
	}
	if _, active := s.activeTopologyRuns.LoadOrStore(run.ID, struct{}{}); active {
		return
	}
	s.background.Go(ctx, func(runCtx context.Context) {
		defer s.activeTopologyRuns.Delete(run.ID)
		_, runErr := s.Orchestrator.Run(runCtx, orchestration.RunRequest{
			SessionID:       sessionID,
			WorkflowID:      run.WorkflowID,
			WorkflowVersion: run.WorkflowVersion,
			Input: map[string]any{
				"project_id":       sess.ProjectID,
				"project_dir":      sess.WorkspacePath,
				"workflow_run_id":  run.ID,
				"workflow_id":      run.WorkflowID,
				"workflow_version": run.WorkflowVersion,
				"blueprint_path":   run.BlueprintPath,
			},
		})
		if runErr != nil && runCtx.Err() == nil {
			slog.ErrorContext(runCtx, "topology settlement failed", "workflow_run_id", run.ID, "err", runErr)
		}
	})
}

// RecoverOrchestratedTopologies restarts settlement for durable running runs.
func (s *Handler) RecoverOrchestratedTopologies(ctx context.Context) error {
	if s.Orchestrator == nil {
		return nil
	}
	runs, err := s.Runs.ListRunning(ctx)
	if err != nil {
		return fmt.Errorf("list running workflow topologies: %w", err)
	}
	for i := range runs {
		s.StartOrchestratedTopologyForRun(ctx, runs[i].SessionID, &runs[i])
	}
	return nil
}
