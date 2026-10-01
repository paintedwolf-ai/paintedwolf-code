package prompts

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/promptunit"
)

// AuthorRenderRequest describes an authored preview.
type AuthorRenderRequest struct {
	ModuleRoot  string         // host configuration root
	ProjectDir  string         // "" = no project overlay
	TemplateRef string         // xor AgentID
	AgentID     string         // renders the full persona through the contract
	Vars        map[string]any // typed facts merged last into the render context
	Check       bool           // also enforce heading contract + byte budget
}

// AuthorRenderResult carries output and stable violations.
type AuthorRenderResult struct {
	Output     string
	Violations []string
	Revision   string
}

// AuthorRender renders a template or persona preview.
func AuthorRender(ctx context.Context, req AuthorRenderRequest) (AuthorRenderResult, error) {
	moduleRoot := strings.TrimSpace(req.ModuleRoot)
	if moduleRoot == "" {
		return AuthorRenderResult{}, fmt.Errorf("module root required")
	}
	ref := strings.TrimSpace(req.TemplateRef)
	agentID := strings.TrimSpace(req.AgentID)
	if ref == "" && agentID == "" {
		return AuthorRenderResult{}, fmt.Errorf("a template ref or --agent is required")
	}
	if ref != "" && agentID != "" {
		return AuthorRenderResult{}, fmt.Errorf("template ref and --agent are mutually exclusive")
	}

	engine := NewFileTemplateEngineLayers(PromptLayers{
		ModuleRoot: moduleRoot,
		Site:       SitePromptFilesDir(moduleRoot),
	})
	if project := strings.TrimSpace(req.ProjectDir); project != "" {
		engine = engine.WithProjectOverlays([]string{project})
	}
	engine, err := engine.Snapshot()
	if err != nil {
		return AuthorRenderResult{}, err
	}

	vars := make(map[string]any, len(req.Vars))
	for k, v := range req.Vars {
		vars[k] = v
	}

	out := AuthorRenderResult{Revision: engine.Revision()}
	if agentID != "" {
		body, err := renderPersona(ctx, engine, agentID, vars, false)
		if err != nil {
			return AuthorRenderResult{}, err
		}
		out.Output = body
	} else {
		if err := mergeAuthorUnits(ctx, engine, vars); err != nil {
			return AuthorRenderResult{}, err
		}
		body, err := engine.Render(ctx, ref, vars)
		if err != nil {
			return AuthorRenderResult{}, err
		}
		out.Output = body
	}

	if !req.Check {
		return out, nil
	}
	violations, err := ValidatePersonaRender(agentID, out.Output)
	if err != nil {
		return AuthorRenderResult{}, err
	}
	out.Violations = violations
	return out, nil
}

// mergeAuthorUnits renders the unit slots a preview's facts select, so a
// template that carries a slot previews the way the host renders it. A tool
// counts as offered when the preview says profile_has_<tool> is true; an
// explicit units var is kept.
func mergeAuthorUnits(ctx context.Context, engine *FileTemplateEngine, vars map[string]any) error {
	if _, ok := vars["units"]; ok {
		return nil
	}
	catalog, err := UnitCatalogForEngine(engine)
	if err != nil {
		return fmt.Errorf("unit catalog: %w", err)
	}
	var offered []string
	for key, value := range vars {
		if name, ok := strings.CutPrefix(key, "profile_has_"); ok && value == true {
			offered = append(offered, name)
		}
	}
	mode, _ := vars["execution_mode"].(string)
	sel := UnitSelectionVars(promptunit.HostCoordinator, mode, offered, offered, nil)
	blocks, err := RenderUnitSlots(ctx, UnitRendererFor(engine), catalog, sel, vars)
	if err != nil {
		return err
	}
	MergeUnitVars(vars, blocks)
	return nil
}

// ValidatePersonaRender checks the final composed persona, not source fragments.
func ValidatePersonaRender(agentID, body string) ([]string, error) {
	violations, err := validatePersonaContractRender(agentID, body)
	if err != nil {
		return nil, err
	}
	budgets, err := LoadPromptBudgets()
	if err != nil {
		return nil, fmt.Errorf("prompt budgets: %w", err)
	}
	if capBytes, ok := budgets.WorkerPersonas[agentID]; ok && capBytes > 0 && len(body) > capBytes {
		violations = append(violations, fmt.Sprintf("budget_exceeded:%s:%d/%d", agentID, len(body), capBytes))
	}
	return violations, nil
}

// validatePersonaContractRender enforces invariants required for a worker to
// run. Per-artifact byte caps stay in ValidatePersonaRender for authoring and
// release checks because runtime render variables can add legitimate content.
func validatePersonaContractRender(agentID, body string) ([]string, error) {
	if agentID == "" {
		return nil, nil
	}
	contract, err := LoadPersonaContract()
	if err != nil {
		return nil, fmt.Errorf("persona contract: %w", err)
	}
	var violations []string
	for _, heading := range contract.RequiredHeadingsRendered {
		heading = strings.TrimSpace(heading)
		if heading == "" {
			continue
		}
		if !strings.Contains(body, heading) {
			violations = append(violations, "missing_heading:"+heading)
		}
	}
	if def, ok := contract.Agents[agentID]; ok {
		for _, must := range def.MustSubstringsRendered {
			must = strings.TrimSpace(must)
			if must != "" && !strings.Contains(body, must) {
				violations = append(violations, "missing_substring:"+must)
			}
		}
	}
	return violations, nil
}
