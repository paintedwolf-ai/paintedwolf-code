package workflow

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSecurityPatchVersionStartsAndReleasedVersionRemainsResolvable(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	run, err := startRun(t.Context(), mgr, "sess-1", "security-survey", "1.0.1")
	testutil.FailErr(t, "start patch workflow", err)
	if run.WorkflowVersion != "1.0.1" {
		t.Fatalf("started version = %s", run.WorkflowVersion)
	}
	if _, err := mgr.StartHuman(t.Context(), "sess-1", api.StartWorkflowRunRequest{WorkflowID: "security-survey", WorkflowVersion: "1.0.0"}); err == nil {
		t.Fatal("retired workflow accepted a new human start")
	}
	current, err := mgr.manifestForRun(t.Context(), run)
	testutil.FailErr(t, "resolve patch run", err)
	challenge, _ := current.PhaseByID("challenge")
	if challenge.ReviewLoop == nil || !challenge.ReviewLoop.CarriesCoverage() {
		t.Fatal("patch run lacks coverage assessment")
	}
	legacy, err := mgr.manifestForRun(t.Context(), &api.WorkflowRun{WorkflowID: "security-survey", WorkflowVersion: "1.0.0", SessionID: "sess-1"})
	testutil.FailErr(t, "resolve released run", err)
	oldChallenge, _ := legacy.PhaseByID("challenge")
	if !legacy.Retired || oldChallenge.ReviewLoop == nil || oldChallenge.ReviewLoop.CarriesCoverage() {
		t.Fatal("released run acquired new requirements")
	}
	registry, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "load versioned catalog", err)
	if !registry.CatalogStartable("security-survey", "1.0.1") || registry.CatalogStartable("security-survey", "1.0.0") {
		t.Fatal("start catalog exposes wrong security version")
	}
	var versions []string
	for _, m := range registry.List() {
		if m.ID == "security-survey" {
			versions = append(versions, m.Version)
		}
	}
	if len(versions) != 2 || versions[0] != "1.0.1" || versions[1] != "1.0.0" {
		t.Fatalf("semantic version ordering = %v", versions)
	}
	rows := FilterProductCatalogSummaries(registry.SummariesWithScopes(map[string]string{}), registry.All())
	found := 0
	for _, row := range rows {
		if row.ID == "security-survey" {
			found++
			if row.Version != "1.0.1" {
				t.Fatalf("start catalog selected %s", row.Version)
			}
		}
	}
	if found != 1 {
		t.Fatalf("start catalog has %d security workflows", found)
	}
}
