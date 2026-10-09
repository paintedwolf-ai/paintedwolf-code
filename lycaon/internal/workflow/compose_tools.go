package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/tools"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// composeToolResponse is the slim tool JSON envelope (no effective_yaml).
type composeToolResponse struct {
	DryRun           bool                        `json:"dry_run"`
	Summary          api.WorkflowSummary         `json:"summary"`
	EffectiveSummary api.ComposeEffectiveSummary `json:"effective_summary"`
}

func marshalComposeToolResponse(result *workflowcomposition.ComposeResult, dryRun bool) (string, error) {
	if result == nil {
		return "", fmt.Errorf("compose result required")
	}
	payload := composeToolResponse{
		DryRun:           dryRun,
		Summary:          result.Summary,
		EffectiveSummary: result.EffectiveSummary,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// RegisterComposeTool registers workflow_compose for coordinator sessions.
func RegisterComposeTool(reg *tools.DefaultRegistry, composer *workflowcomposition.Composer) error {
	if reg == nil || composer == nil {
		return fmt.Errorf("registry and composer required")
	}
	if err := reg.Register("workflow_compose", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !toolguard.IsCoordinatorAgent(tctx.Agent) {
			return "", fmt.Errorf("workflow_compose requires coordinator role")
		}
		manifestYAML, _ := args["manifest_yaml"].(string)
		if strings.TrimSpace(manifestYAML) == "" {
			return "", fmt.Errorf("manifest_yaml required")
		}
		dryRun, _ := args["dry_run"].(bool)
		result, err := composer.Compose(ctx, workflowcomposition.ComposeRequest{
			SessionID:    tctx.SessionID,
			ProjectDir:   tctx.ActiveRootPath(),
			ManifestYAML: []byte(manifestYAML),
			CreatedBy:    workflowdrafts.Coordinator,
			DryRun:       dryRun,
		})
		if err != nil {
			return "", err
		}
		tctx.SetDisplaySubject(result.Summary.Name)
		return marshalComposeToolResponse(result, dryRun)
	}); err != nil {
		return err
	}
	return nil
}

// RegisterComposeFromTemplateTool registers workflow_compose_from_template for coordinators.
func RegisterComposeFromTemplateTool(reg *tools.DefaultRegistry, composer *workflowcomposition.Composer) error {
	if reg == nil || composer == nil {
		return fmt.Errorf("registry and composer required")
	}
	if err := reg.Register("workflow_compose_from_template", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !toolguard.IsCoordinatorAgent(tctx.Agent) {
			return "", fmt.Errorf("workflow_compose_from_template requires coordinator role")
		}
		templateID, _ := args["template_id"].(string)
		if strings.TrimSpace(templateID) == "" {
			return "", fmt.Errorf("template_id required")
		}
		params, _ := args["params"].(map[string]any)
		if params == nil {
			params = map[string]any{}
		}
		dryRun, _ := args["dry_run"].(bool)
		result, err := composer.ComposeFromTemplate(ctx, workflowcomposition.ComposeFromTemplateRequest{
			SessionID:  tctx.SessionID,
			ProjectDir: tctx.ActiveRootPath(),
			TemplateID: templateID,
			Params:     params,
			CreatedBy:  workflowdrafts.Coordinator,
			DryRun:     dryRun,
		})
		if err != nil {
			return "", err
		}
		tctx.SetDisplaySubject(result.Summary.Name)
		return marshalComposeToolResponse(result, dryRun)
	}); err != nil {
		return err
	}
	return nil
}
