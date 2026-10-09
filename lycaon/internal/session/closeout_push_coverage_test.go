package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"gopkg.in/yaml.v3"
)

const (
	openPlanFixture     = "## Progress\n- [x] survey\n- [ ] review"
	terminalPlanFixture = "## Progress\n- [x] survey\n- [x] review"
)

// implementSurfaceUniverse lists every implement-family coordinator surface.
var implementSurfaceUniverse = []string{
	spawn.SurfaceImplementSynthesis,
	spawn.SurfaceImplementRouting,
	surface.SurfaceImplementDispatch,
	surface.SurfaceImplementOverlayPromote,
	surface.SurfaceImplementPark,
	tools.SurfaceImplementInvestigate,
}

// TestCloseoutPushCoversEveryReachableProseSurface covers open-progress closeout routing.
func TestCloseoutPushCoversEveryReachableProseSurface(t *testing.T) {
	empty := surface.ImplementSessionState{}

	// Open progress keeps synthesis unreachable.
	if workeroutcomes.BatchReadyForSynthesis(empty, nil, openPlanFixture, false, false) {
		t.Fatal("an open plan must keep the synthesis surface unreachable (G5 gates entry on AllTerminal)")
	}
	if !workeroutcomes.SynthesisBlockedOnlyByOpenProgress(empty, nil, openPlanFixture, false, false) {
		t.Fatal("an otherwise-ready batch with an open plan must be the blocked-only-by-open-progress wrap-up moment")
	}
	if !workeroutcomes.BatchReadyForSynthesis(empty, nil, terminalPlanFixture, false, false) {
		t.Fatal("a terminal plan with all other gates satisfied must reach the synthesis surface")
	}

	assertUniverseCoversRuntimeImplementSurfaces(t)

	var fired []string
	for _, surfaceID := range implementSurfaceUniverse {
		mgr, sess := newSynthesisDelayManager(t)
		mgr.progress.Set(sess.ID, openPlanFixture)

		_, block := rejectCloseout(t, mgr, sess, surfaceID, true)

		reachableWhileOpen := guard.SurfaceFinishesWithUserProse(surfaceID) &&
			surfaceID != spawn.SurfaceImplementSynthesis
		if block != reachableWhileOpen {
			t.Fatalf("surface %q: closeout push fired=%v, want %v (prose-closeout=%v, synthesis-excluded=%v)",
				surfaceID, block, reachableWhileOpen,
				guard.SurfaceFinishesWithUserProse(surfaceID), surfaceID == spawn.SurfaceImplementSynthesis)
		}
		if block {
			fired = append(fired, surfaceID)
		}
	}

	if len(fired) == 0 {
		t.Fatal("the open-plan closeout push fired on no reachable surface — a wrap-up turn could deliver with the plan still open")
	}
}

// assertUniverseCoversRuntimeImplementSurfaces keeps the fixture aligned with runtime surfaces.
func assertUniverseCoversRuntimeImplementSurfaces(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "host", "coordinator-surfaces.yaml"))
	testutil.FailErr(t, "read coordinator-surfaces.yaml", err)
	var doc map[string]any
	testutil.FailErr(t, "parse coordinator-surfaces.yaml", yaml.Unmarshal(data, &doc))

	known := make(map[string]struct{}, len(implementSurfaceUniverse))
	for _, s := range implementSurfaceUniverse {
		known[s] = struct{}{}
	}
	for key := range doc {
		if !strings.HasPrefix(key, "implement_") {
			continue
		}
		if _, ok := known[key]; !ok {
			t.Fatalf("coordinator-surfaces.yaml defines implement surface %q not in implementSurfaceUniverse — add it so the closeout-push contract covers it", key)
		}
	}
}
