package contract

import (
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowvalidation "github.com/lycaon/lycaon/internal/workflow/validation"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/workflowfixture"
	"strings"
	"testing"
)

// Every coordinator surface declares one exit class.
func TestCoordinatorSurfaceExitCatalog(t *testing.T) {
	t.Parallel()
	exits, err := surface.AllSurfaceExits()
	contractcheck.FailErr(t, "AllSurfaceExits", err)
	if len(exits) == 0 {
		t.Fatal("no surface exits declared")
	}

	surfaces, err := surface.CompileToolPlans(1)
	contractcheck.FailErr(t, "CompileToolPlans", err)
	for id := range surfaces {
		if _, ok := exits[id]; !ok {
			t.Errorf("surface %q has no exit class", id)
		}
	}

	var reports []string
	for id, exit := range exits {
		if surface.SurfaceDeliversReport(id) != (exit == surface.ExitReport) {
			t.Errorf("closeout guard disagrees with catalog for %q (exit=%s)", id, exit)
		}
		if exit == surface.ExitReport {
			reports = append(reports, id)
		}
	}
	if len(reports) == 0 {
		t.Fatal("no report-exit surfaces declared — closeout machinery is unreachable")
	}
}

// Report-gated phases use report exits.
func TestShippedWorkflowReportPhasesBindReportExitSurfaces(t *testing.T) {
	t.Parallel()
	manifests, err := workflowfixture.LoadMergedWorkflowCatalog(t)
	contractcheck.FailErr(t, "loadMergedWorkflowCatalog", err)
	checked := 0
	for _, m := range manifests {
		for _, p := range m.PhaseDefs {
			if !hasGate(p, "topology_report_delivered") {
				continue
			}
			checked++
			if diags := workflowvalidation.ValidateReportPhaseSurfaceExit(m, p); len(diags) > 0 {
				t.Errorf("%s phases[%s]: %s", m.ID, p.ID, diags[0].Message)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no shipped report phases found — gate name or catalog moved")
	}
}

func hasGate(p workflowdef.PhaseDef, gate string) bool {
	for _, g := range p.Gates {
		if strings.TrimSpace(g) == gate {
			return true
		}
	}
	return false
}
