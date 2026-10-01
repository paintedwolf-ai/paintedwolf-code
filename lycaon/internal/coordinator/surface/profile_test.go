package surface

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolveTurnProfileImplementInvestigateFirstUser(t *testing.T) {
	p := ResolveTurnProfile(api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild}, []api.Message{visibleTurnMessage("Hello")})
	if p.SurfaceID != tools.SurfaceImplementInvestigate {
		t.Fatalf("surface = %q want investigate default", p.SurfaceID)
	}
}

func TestSelectSurfaceInvestigateDefersOperationalTools(t *testing.T) {
	plan, err := CompileToolPlan(TurnProfile{SurfaceID: tools.SurfaceImplementInvestigate}, 1)
	testutil.FailErr(t, "CompileToolPlan", err)
	got := plan.DeferredNames()
	found := map[string]bool{}
	for _, name := range got {
		found[name] = true
	}
	if !found["scan_pack"] {
		t.Fatalf("deferred missing scan_pack: %v", got)
	}
	for _, name := range []string{"capture_page", "page_open", "terminal_open", "terminal_snapshot"} {
		if !found[name] {
			t.Fatalf("deferred investigate tools missing %q: %v", name, got)
		}
	}
	sticky := plan.ImmediateNames()
	stickySet := make(map[string]bool, len(sticky))
	for _, name := range sticky {
		stickySet[name] = true
	}
	for _, name := range []string{"capture_page", "page_open", "terminal_open", "terminal_snapshot"} {
		if stickySet[name] {
			t.Fatalf("sticky investigate schema includes deferred tool %q: %v", name, sticky)
		}
	}
	if stickySet["scan_pack"] {
		t.Fatalf("scan_pack should remain deferred: %v", sticky)
	}
}

func TestCompileToolPlansFromYAML(t *testing.T) {
	plans, err := CompileToolPlans(1)
	testutil.FailErr(t, "CompileToolPlans", err)
	if len(plans) == 0 {
		t.Fatal("expected non-empty coordinator surfaces from YAML")
	}
	for _, id := range []string{"await_user", "plan_research", tools.SurfaceImplementInvestigate} {
		if _, ok := plans[id]; !ok {
			t.Fatalf("missing surface %q", id)
		}
	}
}

func TestCompileToolPlansFailClosedOnEmpty(t *testing.T) {
	// Exclude the bundled catalog.
	configtest.Only(t, map[config.Rel]string{config.CoordinatorSurface: "{}\n"})
	_, err := CompileToolPlans(1)
	if err == nil {
		t.Fatal("expected error for empty coordinator-surfaces.yaml")
	}
}

func TestCompileToolPlansFailClosedWithoutModeRef(t *testing.T) {
	configtest.Only(t, map[config.Rel]string{config.CoordinatorSurface: `
review_adjudicate:
  activity_label: Reviewing
  exit: submit_verdict
  floor: [submit_verdict]
no_folder_allowlist:
  floor: [submit_verdict]
`})
	_, err := CompileToolPlans(1)
	if err == nil || !strings.Contains(err.Error(), "mode_ref is required") {
		t.Fatalf("err = %v, want missing mode_ref rejection", err)
	}
}
