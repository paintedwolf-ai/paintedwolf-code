package contract

import (
	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/extpacks"
	workflowcomposition "github.com/lycaon/lycaon/internal/workflow/composition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"testing"
)

func TestComposePolicyYAMLLoads(t *testing.T) {
	t.Parallel()
	policy, err := workflowcomposition.LoadComposePolicy()
	contractcheck.FailErr(t, "workflowcomposition.LoadComposePolicy failed", err)
	if !policy.RequireExtends {
		t.Fatal("expected require_extends true")
	}
	if policy.MaxPhasesCap() != 5 {
		t.Fatalf("max phases = %d", policy.MaxPhasesCap())
	}
}

func TestBundledWorkflowTemplatesExpand(t *testing.T) {
	t.Parallel()
	catalog, err := workflowcomposition.LoadTemplatesFromDir(extpacks.Bundled(config.PlatformFlows.Join("_templates")))
	contractcheck.FailErr(t, "load workflow templates", err)
	if len(catalog) < 3 {
		t.Fatalf("templates = %d want >= 3", len(catalog))
	}
	for id, tmpl := range catalog {
		params := map[string]any{"workflow_id": "contract-" + id}
		if id == "clarify-then-implement-template" {
			params["question"] = "Which module?"
		}
		yaml, err := tmpl.Expand(params)
		if err != nil {
			t.Fatalf("%s expand: %v", id, err)
		}
		if len(yaml) == 0 {
			t.Fatalf("%s empty yaml", id)
		}
	}
}

func TestWorkflowTemplateRoutesInOpenAPI(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	foundList := false
	foundCompose := false
	for _, r := range routes {
		if r.Method == "GET" && r.Path == "/v1/workflow-templates" && r.OperationID == "listWorkflowTemplates" {
			foundList = true
		}
		if r.Method == "POST" && r.Path == "/v1/sessions/{id}/workflows/compose-from-template" && r.OperationID == "composeWorkflowFromTemplate" {
			foundCompose = true
		}
	}
	if !foundList {
		t.Fatal("listWorkflowTemplates route missing")
	}
	if !foundCompose {
		t.Fatal("composeWorkflowFromTemplate route missing")
	}
}
