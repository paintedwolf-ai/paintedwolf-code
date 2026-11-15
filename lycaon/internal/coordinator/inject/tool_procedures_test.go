package inject_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/promptunit"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolsurface"
)

func testProcedureReader(t *testing.T, profileID string, offered []string) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load profiles", err)
	if mapped, err := prompts.ToolProfileForAgent(profileID); err == nil && mapped != "" {
		profileID = mapped
	}
	loaded := make(map[string]bool, len(offered))
	for _, name := range offered {
		loaded[name] = true
	}
	reader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: profileID,
		Profiles:  profiles,
		Plan:      toolsurface.Compile(offered, nil),
		Loaded:    loaded,
		Visible:   offered,
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "new reader", err)
	return reader
}

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
		t.Context(), renderer, "session",
		testProcedureReader(t, prompts.CoordinatorProfileID, []string{"command", "update_progress"}),
	)
	testutil.FailErr(t, "render tool procedures block", err)
	if !strings.Contains(block, "Snapshot the running UI") || !strings.Contains(block, "Snapshot the terminal") {
		t.Fatalf("rendered tool procedures block missing visual snapshot checklist cue: %q", block)
	}
}

func TestToolProcedureUnitsAreTheUnitsTheBlockRenders(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	catalog, err := renderer.UnitCatalog()
	testutil.FailErr(t, "load instruction catalog", err)
	offered := []string{"edit", "update_progress"}
	units, err := inject.ToolProcedureUnits(renderer, testProcedureReader(t, prompts.CoordinatorProfileID, offered))
	testutil.FailErr(t, "list rendered units", err)
	want := catalog.Rendered(prompts.UnitSelectionVars(promptunit.HostCoordinator, "", offered))
	count := 0
	for _, slotUnits := range want {
		count += len(slotUnits)
	}
	if len(units) != count || count == 0 {
		t.Fatalf("units = %d, rendered = %d", len(units), count)
	}
	attached := 0
	for _, unit := range units {
		if len(unit.Attaches) > 0 {
			attached++
			if !unit.AttachedTo(map[string]bool{"edit": true, "update_progress": true}) {
				t.Fatalf("unit %q attaches none of the offered tools", unit.ID)
			}
		}
	}
	if attached == 0 {
		t.Fatal("no unit attached to the offered tools")
	}
	worker, err := inject.ToolProcedureUnits(renderer, testProcedureReader(t, "implementer", nil))
	testutil.FailErr(t, "list worker units", err)
	for _, unit := range worker {
		if len(unit.Attaches) > 0 || !unit.HostsFor(promptunit.HostWorker) {
			t.Fatalf("worker render with no tools included %q", unit.ID)
		}
	}
}
