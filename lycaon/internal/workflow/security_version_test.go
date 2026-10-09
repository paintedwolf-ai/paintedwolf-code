package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCurrentSecurityVersionStartsAndReleasedVersionRemainsResolvable(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	run, err := startRun(t.Context(), mgr, "sess-1", "security-survey", "2.0.0")
	testutil.FailErr(t, "start current workflow", err)
	if run.WorkflowVersion != "2.0.0" {
		t.Fatalf("started version = %s", run.WorkflowVersion)
	}
	if _, err := mgr.Starts.StartHuman(t.Context(), "sess-1", api.StartWorkflowRunRequest{WorkflowID: "security-survey", WorkflowVersion: "1.0.0"}); err == nil {
		t.Fatal("retired workflow accepted a new human start")
	}
	current, err := mgr.Resolver.ForRun(t.Context(), run)
	testutil.FailErr(t, "resolve current run", err)
	challenge, _ := current.PhaseByID("challenge")
	if challenge.ReviewLoop == nil || !challenge.ReviewLoop.CarriesCoverage() {
		t.Fatal("current run lacks coverage assessment")
	}
	legacy, err := mgr.Resolver.ForRun(t.Context(), &api.WorkflowRun{WorkflowID: "security-survey", WorkflowVersion: "1.0.0", SessionID: "sess-1"})
	testutil.FailErr(t, "resolve released run", err)
	oldChallenge, _ := legacy.PhaseByID("challenge")
	if !legacy.Retired || oldChallenge.ReviewLoop == nil || oldChallenge.ReviewLoop.CarriesCoverage() {
		t.Fatal("released run acquired new requirements")
	}
	if challenge.ReviewLoop.FollowupAttempts != 2 {
		t.Fatal("current workflow lacks bounded follow-up")
	}
	registry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load versioned catalog", err)
	if !registry.CatalogStartable("security-survey", "2.0.0") || registry.CatalogStartable("security-survey", "1.0.0") {
		t.Fatal("start catalog exposes wrong security version")
	}
	var versions []string
	for _, m := range registry.List() {
		if m.ID == "security-survey" {
			versions = append(versions, m.Version)
		}
	}
	if len(versions) != 2 || versions[0] != "2.0.0" || versions[1] != "1.0.0" {
		t.Fatalf("semantic version ordering = %v", versions)
	}
	rows := workflowcatalog.FilterProductCatalogSummaries(registry.SummariesWithScopes(map[string]string{}), registry.All())
	found := 0
	for _, row := range rows {
		if row.ID == "security-survey" {
			found++
			if row.Version != "2.0.0" {
				t.Fatalf("start catalog selected %s", row.Version)
			}
		}
	}
	if found != 1 {
		t.Fatalf("start catalog has %d security workflows", found)
	}
}

func TestRetiredSecurityRunProjectsItsSelectedDefinition(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	run, err := startRun(t.Context(), mgr, "sess-1", "security-survey", "2.0.0")
	testutil.FailErr(t, "start current workflow", err)
	run.WorkflowVersion = "1.0.0"
	ui, err := mgr.Presentation.ComputeRunUI(t.Context(), run)
	testutil.FailErr(t, "project retired workflow", err)
	if ui.Definition == nil || ui.Definition.Version != "1.0.0" || ui.Definition.ID != "security-survey" || len(ui.Definition.Phases) == 0 {
		t.Fatalf("selected definition = %+v", ui.Definition)
	}
}
