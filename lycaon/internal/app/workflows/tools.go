package workflows

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/blueprint"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	workflowstatetools "github.com/lycaon/lycaon/internal/workflow/statetools"
)

// RegisterTools registers state, plan, compose, feedback, ask_user, advance, transition, fanout, and verdict tools.
func (r *Runtime) RegisterTools(reg *tools.DefaultRegistry, boundary *sandbox.Boundary, sessions session.Store, surfaceGate *settings.ProjectSurfaceGate) error {
	if err := workflowstatetools.RegisterStateTools(reg, workflowstatetools.StateToolDeps{
		Runs:     r.Store.Runs,
		Vars:     r.Manager.Phases.Vars,
		Journal:  r.Manager.Phases.Journal,
		Resolver: &r.Manager.Resolver,
		Starts:   r.Manager.Starts,
		Controls: r.Manager.Controls,
		Scaffold: r.Manager.Blueprints.Scaffold,
		Sessions: sessions,
	}); err != nil {
		return fmt.Errorf("state tools: %w", err)
	}

	if err := blueprint.RegisterPlanTools(reg, r.Blueprints); err != nil {
		return fmt.Errorf("plan tools: %w", err)
	}

	if err := workflow.RegisterComposeTool(reg, r.Composer); err != nil {
		return fmt.Errorf("workflow_compose tool: %w", err)
	}
	if err := workflow.RegisterComposeFromTemplateTool(reg, r.Composer); err != nil {
		return fmt.Errorf("workflow_compose_from_template tool: %w", err)
	}

	var appliesPath func(context.Context, string) bool
	if surfaceGate != nil {
		appliesPath = surfaceGate.AppliesPath
	}
	catalogResolver := workflowcatalog.Resolver{
		SessionStore:       r.Drafts,
		ProjectTierApplies: appliesPath,
	}
	if err := workflow.RegisterCatalogSummariesTool(reg, catalogResolver, r.Drafts, r.Composer.Templates); err != nil {
		return fmt.Errorf("workflow_catalog_summaries tool: %w", err)
	}
	if err := workflow.RegisterPersistTool(reg, r.Persister); err != nil {
		return fmt.Errorf("workflow_persist tool: %w", err)
	}
	if err := workflowinputs.RegisterFeedbackTool(reg, r.Manager.Feedback); err != nil {
		return fmt.Errorf("workflow_user_feedback tool: %w", err)
	}
	if err := workflowinputs.RegisterAskUserTool(reg, r.Manager.Asks, boundary); err != nil {
		return fmt.Errorf("ask_user tool: %w", err)
	}
	if err := workflowphases.RegisterAdvanceTool(reg, r.Manager.Phases); err != nil {
		return fmt.Errorf("workflow_advance tool: %w", err)
	}
	if err := workflowphases.RegisterTransitionTool(reg, r.Manager.Phases); err != nil {
		return fmt.Errorf("workflow_transition tool: %w", err)
	}
	if err := workflow.RegisterFanoutPlanTool(reg, r.Manager.Fanout); err != nil {
		return fmt.Errorf("fanout_plan tool: %w", err)
	}
	if err := workflowreview.RegisterSubmitVerdictTool(reg, r.Manager.Verdicts); err != nil {
		return fmt.Errorf("submit_verdict tool: %w", err)
	}
	return nil
}
