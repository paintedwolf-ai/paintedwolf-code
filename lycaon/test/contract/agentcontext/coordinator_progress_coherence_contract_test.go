package contract

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	coordinatorcoordinationplane "github.com/lycaon/lycaon/test/wiring/fixtures/coordinator_coordination_plane"
)

// TestCoordinatorProgressOneBoardAuthoringTool locks the invariant that plan authoring
// names update_progress as the sole progress writer — no shadow file-board paths.
func TestCoordinatorProgressOneBoardAuthoringTool(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	investigate := renderCoordinatorTripartiteForRunContext(
		t, root, apiCoordinatorRunRunning(), nil, "Survey this repo", nil,
		tools.SurfaceImplementInvestigate,
	)
	if !strings.Contains(investigate, progress.AuthoringToolID) {
		t.Fatalf("investigate prompt must name %q for plan authoring", progress.AuthoringToolID)
	}
	for _, shadow := range []string{settingsoverlay.Rel("plans/board.md"), settingsoverlay.Rel("plans/board.plan.md"), settingsoverlay.Rel("workbook/workbook.md"), settingsoverlay.Rel("scratchpad/board.md")} {
		if strings.Contains(investigate, shadow) {
			t.Fatalf("investigate prompt must not reference shadow board path %q", shadow)
		}
	}
}

// TestCoordinatorProgressShadowBoardPathsNotAuthoringTarget asserts shadow paths are
// flagged and not treated as alternate board locations on plan-authoring surfaces.
func TestCoordinatorProgressShadowBoardPathsNotAuthoringTarget(t *testing.T) {
	t.Parallel()
	for _, shadow := range []string{settingsoverlay.Rel("plans/board.md"), settingsoverlay.Rel("plans/board.plan.md")} {
		if !progress.IsShadowBoardPath(shadow) {
			t.Fatalf("%q must be a shadow board path", shadow)
		}
	}
	if progress.IsShadowBoardPath(settingsoverlay.Rel("plans/notes.plan.md")) {
		t.Fatal(settingsoverlay.Rel("plans/*.plan.md notes must not be shadow boards"))
	}
}

// TestProgressBootstrapRefreshesOnNewRun verifies that a prior goal is replaced
// when the workflow run changes.
func TestProgressBootstrapRefreshesOnNewRun(t *testing.T) {
	t.Parallel()
	fix := coordinatorcoordinationplane.Load(t)
	store := progress.NewMemoryStore()
	const sessionID = "parent-session"
	store.EnsureRun(sessionID, fix.PriorRunID, fix.PriorGoal)
	store.Set(sessionID, fix.StaleProgressContent)

	if refreshed := progress.EnsureBootstrap(t.Context(), store, sessionID, fix.NewRunID, fix.UserPrompt); !refreshed {
		t.Fatal("expected bootstrap refresh on new run")
	}
	got := store.Get(t.Context(), sessionID)
	if strings.Contains(got, fix.PriorGoal) {
		t.Fatalf("prior goal survived refresh: %q", got)
	}
	if !strings.Contains(got, "Survey this repo") {
		t.Fatalf("new goal missing from refreshed content: %q", got)
	}
	if strings.Contains(got, "Map existing auth hooks") {
		t.Fatalf("stale plan steps survived refresh: %q", got)
	}
}

func apiCoordinatorRunRunning() api.CoordinatorRunContext {
	return api.CoordinatorRunContext{RunStatus: "running"}
}
