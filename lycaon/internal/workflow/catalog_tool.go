package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/workflow/toolguard"

	"github.com/lycaon/lycaon/internal/tools"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
	"github.com/lycaon/lycaon/pkg/api"
)

// RegisterCatalogSummariesTool registers workflow_catalog_summaries for coordinator sessions.
func RegisterCatalogSummariesTool(reg *tools.DefaultRegistry, resolver workflowcatalog.Resolver, sessionStore workflowdrafts.Store, templates workflowcomposition.TemplateCatalog) error {
	if reg == nil {
		return fmt.Errorf("registry required")
	}
	if err := reg.Register("workflow_catalog_summaries", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if !toolguard.IsCoordinatorAgent(tctx.Agent) {
			return "", fmt.Errorf("workflow_catalog_summaries requires coordinator role")
		}
		bundled, err := resolver.ListResolved(ctx, tctx.ActiveRootPath(), tctx.SessionID)
		if err != nil {
			return "", err
		}
		sessionRows := []api.WorkflowSummary(nil)
		if sessionStore != nil && tctx.SessionID != "" {
			records, err := sessionStore.ListBySession(ctx, tctx.SessionID)
			if err != nil {
				return "", err
			}
			for _, rec := range records {
				m, err := workflowdef.ParseManifestYAML([]byte(rec.ManifestYAML))
				if err != nil {
					continue
				}
				summary := m.Summary()
				summary.Scope = api.WorkflowScopeSession
				sessionRows = append(sessionRows, summary)
			}
		}
		templateList := []api.WorkflowTemplateSummary(nil)
		if templates != nil {
			templateList = templates.List()
		}
		out := map[string]any{
			"bundled_workflows": bundled,
			"session_workflows": sessionRows,
			"templates":         templateList,
		}
		raw, _ := json.Marshal(out)
		return string(raw), nil
	}); err != nil {
		return err
	}
	return nil
}
