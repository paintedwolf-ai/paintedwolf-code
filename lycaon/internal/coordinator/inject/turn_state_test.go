package inject_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderTurnStateBlock(t *testing.T) {
	root := testConfigRoot(t)
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}))

	t.Run("empty inputs render empty", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-empty", inject.TurnStateInjectInput{})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if block != "" {
			t.Fatalf("expected empty block, got: %q", block)
		}
	})

	t.Run("progress missing with update_progress", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-progress", inject.TurnStateInjectInput{
			ProgressMissing:          true,
			ProfileHasUpdateProgress: true,
		})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if !strings.Contains(block, "No checklist is stored") {
			t.Fatalf("expected checklist warning, got: %q", block)
		}
	})

	t.Run("progress missing without update_progress", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-progress-no-tool", inject.TurnStateInjectInput{
			ProgressMissing:          true,
			ProfileHasUpdateProgress: false,
		})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if strings.Contains(block, "No checklist is stored") {
			t.Fatalf("unexpected checklist warning without tool, got: %q", block)
		}
	})

	t.Run("pending overlays and partial jobs", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-overlays", inject.TurnStateInjectInput{
			PendingOverlayJobs: []string{"job-a", "job-b"},
			PartialWorkerJobs:  []string{"job-c"},
		})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if !strings.Contains(block, "Integrate turn") {
			t.Fatalf("missing integrate turn guidance: %q", block)
		}
		if !strings.Contains(block, "per-overlay promote order") {
			t.Fatalf("missing promote order guidance: %q", block)
		}
		if !strings.Contains(block, "Pending overlay `job-a`, `job-b`") {
			t.Fatalf("missing pending overlay jobs: %q", block)
		}
		if !strings.Contains(block, "Partial `job-c`: `task(implementer)`") {
			t.Fatalf("missing partial worker jobs: %q", block)
		}
	})

	t.Run("verify required", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-verify", inject.TurnStateInjectInput{
			VerifyRequired: true,
		})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if !strings.Contains(block, "This workflow has an explicit test-evidence gate") {
			t.Fatalf("missing test-evidence gate: %q", block)
		}
	})
}

func TestTurnStateCarriesCurrentProgressSnapshot(t *testing.T) {
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: testConfigRoot(t)}))
	snapshot := `{"content":"- [ ] Deliver","revision":"current-snapshot-revision","reconciliation":{"token":"worker-finish-token","jobs":["worker-one"]}}`
	block, err := inject.RenderTurnStateBlock(t.Context(), renderer, "snapshot", inject.TurnStateInjectInput{ProgressSnapshot: snapshot, ProfileHasUpdateProgress: true})
	testutil.FailErr(t, "render current progress", err)
	for _, value := range []string{"current-snapshot-revision", "worker-finish-token", "worker-one", "Deliver"} {
		if !strings.Contains(block, value) {
			t.Fatalf("snapshot omitted %q: %s", value, block)
		}
	}
}
