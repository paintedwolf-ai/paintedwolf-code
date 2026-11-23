package contract

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/rules"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

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
	if _, err := manifests.Get("hotfix", "1.0.0"); err != nil {
		t.Fatalf("persisted overlay hotfix@1.0.0 not in the registry: %v", err)
	}
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{})
	contractcheck.FailErr(t, "build conditions registry", err)
	if err := rules.RegisterRuleConditions(reg); err != nil {
		contractcheck.FailErr(t, "register rule conditions", err)
	}
	ruleConfigs, err := rules.LoadBundledRuleConfigs()
	contractcheck.FailErr(t, "rules.LoadBundledRuleConfigs failed", err)
	if diags := workflowvalidation.ValidateBundled(reg, manifests, ruleConfigs); len(diags) > 0 {
		t.Fatalf("workflowvalidation.ValidateBundled failed: %v", diags)
	}
}
