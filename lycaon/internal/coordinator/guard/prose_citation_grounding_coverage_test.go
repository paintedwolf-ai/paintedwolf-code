package guard_test

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/guard"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/spawn"
	"gopkg.in/yaml.v3"
)

// proseGroundingSurfaceUniverse is every implement-family coordinator surface a host turn can
// land on: the runtime surface keys plus the code-defined surface consts. The closure assertion
// fails when a runtime surface is added without extending this list.
func proseGroundingSurfaceUniverse(t *testing.T) []string {
	t.Helper()
	known := map[string]struct{}{
		spawn.SurfaceImplementSynthesis:          {},
		spawn.SurfaceImplementRouting:            {},
		surface.SurfaceImplementDispatch:         {},
		surface.SurfaceImplementOverlayPromote:   {},
		surface.SurfaceImplementPark:             {},
		toolcontract.SurfaceImplementInvestigate: {},
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "host", "coordinator-surfaces.yaml"))
	if err != nil {
		t.Fatalf("read coordinator-surfaces.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse coordinator-surfaces.yaml: %v", err)
	}
	for key := range doc {
		if len(key) < len("implement_") || key[:len("implement_")] != "implement_" {
			continue
		}
		if _, ok := known[key]; !ok {
			t.Fatalf("coordinator-surfaces.yaml defines implement surface %q absent from the prose-grounding coverage universe — add it so its grounding is checked", key)
		}
	}

	out := make([]string, 0, len(known))
	for s := range known {
		out = append(out, s)
	}
	return out
}

// TestProjectCoordinatorCloseoutProse pins the host-side projection that turns a coordinator
// closeout envelope into the narrative the transcript shows. Without it the raw
// `{"synthesis", "cited_evidence", "cited_urls"}` JSON renders verbatim to the user.
func TestProjectCoordinatorCloseoutProse(t *testing.T) {
	t.Parallel()

	const envelope = `{"synthesis":"The repo follows best practices.","cited_evidence":[{"path":"a.go","line":1,"excerpt":"x"}],"cited_urls":["https://go.dev"]}`

	t.Run("prose-closeout surface projects synthesis", func(t *testing.T) {
		t.Parallel()
		prose, ok := guard.ProjectCoordinatorCloseoutProse(toolcontract.SurfaceImplementInvestigate, envelope)
		if !ok {
			t.Fatal("expected projection on a prose-closeout surface")
		}
		if prose != "The repo follows best practices." {
			t.Fatalf("projected prose = %q, want the synthesis narrative only", prose)
		}
	})

	t.Run("fenced envelope projects synthesis", func(t *testing.T) {
		t.Parallel()
		prose, ok := guard.ProjectCoordinatorCloseoutProse(spawn.SurfaceImplementSynthesis, "```json\n"+envelope+"\n```")
		if !ok || prose != "The repo follows best practices." {
			t.Fatalf("fenced envelope projected = %q, ok=%v", prose, ok)
		}
	})

	t.Run("non-closeout surface passes through", func(t *testing.T) {
		t.Parallel()
		if _, ok := guard.ProjectCoordinatorCloseoutProse(surface.SurfaceImplementOverlayPromote, envelope); ok {
			t.Fatal("overlay-promote closes via a tool — it must not project closeout prose")
		}
	})

	t.Run("non-envelope prose is not projected", func(t *testing.T) {
		t.Parallel()
		if _, ok := guard.ProjectCoordinatorCloseoutProse(toolcontract.SurfaceImplementInvestigate, "Plain prose, no envelope."); ok {
			t.Fatal("non-envelope content must not be projected")
		}
	})

	t.Run("hybrid markdown plus json rejected", func(t *testing.T) {
		t.Parallel()
		leading := "## Code-quality survey\n\nDuplicate constants across models."
		content := leading + "\n\n" + envelope
		if _, ok := guard.ProjectCoordinatorCloseoutProse(toolcontract.SurfaceImplementInvestigate, content); ok {
			t.Fatal("hybrid closeout must not project")
		}
	})
}

// Prose completion and grounding share one surface predicate.
func TestProseFinishSurfacesAreAlwaysGrounded(t *testing.T) {
	t.Parallel()

	groundedProse := 0
	for _, surfaceID := range proseGroundingSurfaceUniverse(t) {
		if guard.SurfaceFinishesWithUserProse(surfaceID) {
			groundedProse++
		}
	}
	if groundedProse == 0 {
		t.Fatal("no prose-finish surface is grounded — the coverage property is checking nothing")
	}

	// Tool-based completion is outside the prose predicate.
	if guard.SurfaceFinishesWithUserProse(surface.SurfaceImplementOverlayPromote) {
		t.Fatal("overlay-promote closes via a tool, not user prose — it must not be a citation-grounding surface")
	}
}
