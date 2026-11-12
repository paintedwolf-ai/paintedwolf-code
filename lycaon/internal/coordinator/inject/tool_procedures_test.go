package inject_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestToolProceduresRenderTheUnitsTheirOfferedToolsAttach(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	catalog, err := renderer.UnitCatalog()
	testutil.FailErr(t, "load instruction catalog", err)
	selection := promptunit.Selection{Host: promptunit.HostCoordinator, Offered: map[string]bool{"edit": true, "update_progress": true}}
	rendered := catalog.Rendered(selection)
	for _, id := range []string{"coordinator-progress-procedure", "visual-show-the-design"} {
		found := false
		for _, units := range rendered {
			for _, unit := range units {
				found = found || unit.ID == id
			}
		}
		if !found {
			t.Fatalf("offered tools did not render unit %q", id)
		}
	}
	block, err := inject.RenderToolProceduresBlock(
		t.Context(), renderer, "session", prompts.CoordinatorProfileID,
		[]string{"command", "update_progress"},
	)
	testutil.FailErr(t, "render tool procedures block", err)
	if !strings.Contains(block, "Snapshot the running UI") || !strings.Contains(block, "Snapshot the terminal") {
		t.Fatalf("rendered tool procedures block missing visual snapshot checklist cue: %q", block)
	}
}
