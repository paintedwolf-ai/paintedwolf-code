package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/vocabulary"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestOpenAPIPersistRoute(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	found := false
	for _, r := range routes {
		if r.Method == "POST" && r.Path == "/v1/sessions/{id}/workflows/{workflow_id}/persist" && r.OperationID == "persistWorkflow" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("persistWorkflow route missing from openapi")
	}
}

func TestPersistedFixturePassesVocabularyLoad(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	fixture := filepath.Join(root, "lycaon", "test", "contract", "testdata", "workflow", "persist_hotfix.yaml")
	data, err := os.ReadFile(fixture)
	contractcheck.FailErr(t, "read file", err)
	projectDir := t.TempDir()
	overlayDir := filepath.Join(projectDir, settingsoverlay.DirName(), "workflows", "hotfix")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		contractcheck.FailErr(t, "create directory", err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "workflow.yaml"), data, 0o644); err != nil {
		contractcheck.FailErr(t, "write file", err)
	}
	manifests, err := workflowdef.RegistryFromDirs(projectDir)
	contractcheck.FailErr(t, "workflow.RegistryFromDirs failed", err)
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		contractcheck.FailErr(t, "register rule conditions", err)
	}
	ruleConfigs, err := rules.LoadBundledRuleConfigs()
	contractcheck.FailErr(t, "rules.LoadBundledRuleConfigs failed", err)
	if diags := vocabulary.ValidateBundled(reg, manifests, ruleConfigs); len(diags) > 0 {
		t.Fatalf("vocabulary.ValidateBundled failed: %v", diags)
	}
}
