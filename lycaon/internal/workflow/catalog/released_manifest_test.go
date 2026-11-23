package catalog

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestReleasedProjectManifestReopens(t *testing.T) {
	data, err := os.ReadFile("testdata/v1.0.2/workflow.yaml")
	testutil.FailErr(t, "read released project workflow", err)
	project := t.TempDir()
	dir := filepath.Join(project, settingsoverlay.DirName(), "workflows", "hotfix")
	testutil.FailErr(t, "create project", os.MkdirAll(dir, 0700))
	testutil.FailErr(t, "copy released workflow", os.WriteFile(filepath.Join(dir, "workflow.yaml"), data, 0600))
	resolver := Resolver{ProjectTierApplies: func(context.Context, string) bool { return true }}
	m, err := resolver.ForSession(t.Context(), project, "", "hotfix", "1.0.0")
	testutil.FailErr(t, "open released workflow", err)
	phase, ok := m.PhaseByID("build")
	if !ok || m.Trigger != "/hotfix-custom" || !slices.Equal(phase.Gates, []string{"delegation_closeout_complete", "evidence_passed:verify"}) {
		t.Fatalf("released build phase = %+v, trigger = %s", phase, m.Trigger)
	}
	catalog, err := workflowdef.RegistryFromDirs(project)
	testutil.FailErr(t, "load released catalog", err)
	_, err = catalog.Get("hotfix", "1.0.0")
	testutil.FailErr(t, "find released workflow", err)
}
