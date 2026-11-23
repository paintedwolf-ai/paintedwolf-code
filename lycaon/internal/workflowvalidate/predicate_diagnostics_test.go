package workflowvalidate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflowvalidate"
)

func TestAuthoredPredicateDiagnosticsSurviveCatalogLoading(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(settingsoverlay.Dir(project), "workflows", "custom-bad")
	testutil.FailErr(t, "create workflow directory", os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, "workflow.yaml")
	testutil.FailErr(t, "write workflow", os.WriteFile(path, []byte("workflow_format: 2\n"+thirdPartyDomainLeaf), 0o600))

	for _, mode := range []workflowvalidate.CatalogValidateMode{workflowvalidate.ModePaths, workflowvalidate.ModeProject} {
		issues, err := workflowvalidate.ValidateCatalog(t.Context(), workflowvalidate.CatalogValidateOptions{
			ConfigRoot: configlayout.FindModuleRoot(), ProjectDir: project, Paths: []string{path}, Mode: mode,
		})
		testutil.FailErr(t, "validate catalog", err)
		if len(issues) != 1 || issues[0].Code != "domain_leaf_on_overlay" ||
			issues[0].Field != path+": phases[only].gates[0]" || issues[0].Replacement == "" {
			t.Errorf("mode %v returned %+v", mode, issues)
		}
	}
}
