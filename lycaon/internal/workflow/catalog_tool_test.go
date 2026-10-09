package workflow_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	workflowdrafts "github.com/lycaon/lycaon/internal/workflow/drafts"
)

func TestWorkflowCatalogSummariesTool(t *testing.T) {
	templates, err := workflowcomposition.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	testutil.FailErr(t, "load workflow templates", err)
	reg := tools.NewDefaultRegistry()
	store := workflowdrafts.NewMemory()
	resolver := workflowcatalog.Resolver{SessionStore: store}
	if err := workflow.RegisterCatalogSummariesTool(reg, resolver, store, templates); err != nil {
		testutil.FailErr(t, "workflow.RegisterCatalogSummariesTool failed", err)
	}
	dir := t.TempDir()
	roots := []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}
	out, err := reg.Run(context.Background(), "workflow_catalog_summaries", map[string]any{}, tools.ToolContext{
		SessionID: "sess-1", Roots: roots, ActiveRootID: "r1", Agent: "coordinator",
	})
	testutil.FailErr(t, "reg.Run failed", err)
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	templatesAny, ok := payload["templates"].([]any)
	if !ok || len(templatesAny) < 3 {
		t.Fatalf("templates = %v", payload["templates"])
	}
	bundledAny, ok := payload["bundled_workflows"].([]any)
	if !ok {
		t.Fatalf("bundled_workflows = %T", payload["bundled_workflows"])
	}
	hasPlan, hasBugbash, hasOptions := false, false, false
	for _, raw := range bundledAny {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("bundled row = %T", raw)
		}
		id, _ := row["id"].(string)
		switch id {
		case "plan":
			hasPlan = true
		case "bugbash":
			hasBugbash = true
			if row["trigger"] != "/bugbash" {
				t.Fatalf("bugbash trigger = %v want /bugbash", row["trigger"])
			}
		case "options":
			hasOptions = true
			if row["trigger"] != "/options" {
				t.Fatalf("options trigger = %v want /options", row["trigger"])
			}
		case "implement":
			t.Fatalf("bundled_workflows must not include %q in product catalog", id)
		}
	}
	if !hasPlan {
		t.Fatalf("bundled_workflows missing plan: %+v", bundledAny)
	}
	if !hasBugbash || !hasOptions {
		t.Fatalf("bundled_workflows missing launch workflows: bugbash=%v options=%v", hasBugbash, hasOptions)
	}
}
