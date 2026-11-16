package inject_test

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderTurnStateBlock(t *testing.T) {
	root := testConfigRoot(t)
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}))

	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	plans, err := surface.CompileToolPlans(1)
	testutil.FailErr(t, "CompileToolPlans", err)
	withProgress, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Profiles:  profiles,
		Plan:      plans["implement_investigate"],
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "NewReader implement_investigate", err)
	withoutProgress, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		Profiles:  profiles,
		Visible:   []string{"read_file"},
		Loaded:    map[string]bool{"read_file": true},
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "NewReader without progress", err)

	t.Run("empty inputs render empty", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-empty", inject.TurnStateInjectInput{})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if block != "" {
			t.Fatalf("expected empty block, got: %q", block)
		}
	})

	t.Run("progress missing with update_progress", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-progress", inject.TurnStateInjectInput{
			ProgressMissing: true,
			Reader:          withProgress,
		})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
		if !strings.Contains(block, "No checklist is stored") {
			t.Fatalf("expected checklist warning, got: %q", block)
		}
	})

	t.Run("progress missing without update_progress", func(t *testing.T) {
		block, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-progress-no-tool", inject.TurnStateInjectInput{
			ProgressMissing: true,
			Reader:          withoutProgress,
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
		if !strings.Contains(block, "Pending overlay `job-a`, `job-b`") {
			t.Fatalf("missing pending overlay jobs: %q", block)
		}
		if !strings.Contains(block, "Partial `job-c`: dispatch an implementer") {
			t.Fatalf("missing partial worker jobs: %q", block)
		}
	})

	t.Run("verify required", func(t *testing.T) {
		_, err := inject.RenderTurnStateBlock(context.Background(), renderer, "sess-verify", inject.TurnStateInjectInput{
			VerifyRequired: true,
		})
		testutil.FailErr(t, "RenderTurnStateBlock", err)
	})
}

func TestTurnStateCarriesCurrentProgressSnapshot(t *testing.T) {
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: testConfigRoot(t)}))
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	plans, err := surface.CompileToolPlans(1)
	testutil.FailErr(t, "CompileToolPlans", err)
	withProgress, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Profiles:  profiles,
		Plan:      plans["implement_investigate"],
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "NewReader implement_investigate", err)
	snapshot := `{"content":"- [ ] Deliver","revision":"current-snapshot-revision","reconciliation":{"token":"worker-finish-token","jobs":["worker-one"]}}`
	block, err := inject.RenderTurnStateBlock(t.Context(), renderer, "snapshot", inject.TurnStateInjectInput{ProgressSnapshot: snapshot, Reader: withProgress})
	testutil.FailErr(t, "render current progress", err)
	for _, value := range []string{"current-snapshot-revision", "worker-finish-token", "worker-one", "Deliver"} {
		if !strings.Contains(block, value) {
			t.Fatalf("snapshot omitted %q: %s", value, block)
		}
	}
}
