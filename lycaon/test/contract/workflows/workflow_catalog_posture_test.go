package contract

import (
	"github.com/lycaon/lycaon/internal/session"
	sessionposture "github.com/lycaon/lycaon/internal/session/posture"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
	"testing"
)

func TestWorkflowCatalogSummaryPosturesValid(t *testing.T) {
	t.Parallel()
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	for key, m := range catalog {
		summary := m.Summary()
		if ip := m.InitialPosture; ip != "" {
			if summary.InitialPosture != ip {
				t.Errorf("%s: summary initial_posture = %q manifest = %q", key, summary.InitialPosture, ip)
			}
			if !sessionposture.ValidSessionPosture(ip) {
				t.Errorf("%s: invalid initial_posture %q", key, ip)
			}
		}
		if summary.InitialPosture != "" && !sessionposture.ValidSessionPosture(summary.InitialPosture) {
			t.Errorf("%s: summary exposes invalid posture %q", key, summary.InitialPosture)
		}
	}
}

func TestDefaultRegistrySummariesExposeValidPostures(t *testing.T) {
	t.Parallel()
	reg := contractcheck.CatalogRegistry(t)
	for _, summary := range reg.SummariesWithScopes(nil) {
		if summary.InitialPosture == "" {
			t.Errorf("workflow %s@%s missing initial_posture in catalog summary", summary.ID, summary.Version)
			continue
		}
		if !sessionposture.ValidSessionPosture(summary.InitialPosture) {
			t.Errorf("workflow %s@%s summary posture = %q invalid", summary.ID, summary.Version, summary.InitialPosture)
		}
	}
}

func TestBundledPlanManifestMatchesSessionPostureRegistry(t *testing.T) {
	t.Parallel()
	reg, err := session.LoadPostureRegistry()
	contractcheck.FailErr(t, "session.LoadPostureRegistry failed", err)
	catalog, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog failed", err)
	plan, ok := catalog["plan@1.0.0"]
	if !ok {
		t.Fatal("missing plan@1.0.0")
	}
	if plan.InitialPosture != string(api.SessionPostureSpec) {
		t.Fatalf("plan initial_posture = %q", plan.InitialPosture)
	}
	if _, err := reg.Get(api.SessionPostureSpec); err != nil {
		t.Fatalf("registry missing spec posture: %v", err)
	}
	execute, ok := plan.PhaseByID("execute")
	if !ok {
		t.Fatal("missing execute phase")
	}
	if execute.InvokeWorkflow == nil || execute.InvokeWorkflow.WorkflowID != "implement" {
		t.Fatalf("execute subroutine = %+v", execute.InvokeWorkflow)
	}
	if _, err := reg.Get(api.SessionPostureBuild); err != nil {
		t.Fatalf("registry missing build posture: %v", err)
	}
}
