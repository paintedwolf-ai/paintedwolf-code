package inject_test

import (
	"github.com/lycaon/lycaon/internal/coordinator/inject"

	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func boardTestCoordReader(t *testing.T) prompts.Reader {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	r, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  profiles,
		RootCount: 1,
		HasGit:    true,
		WebSearch: true,
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		Declared:  true,
	})
	testutil.FailErr(t, "NewReader coordinator", err)
	return r
}

func TestRenderBoardOrientationInject(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	reader := boardTestCoordReader(t)
	block, err := inject.RenderBoardOrientationInject(
		context.Background(),
		renderer,
		reader,
		"sess-inject-test",
		api.BoardSnapshot{PackContentHash: "h1"},
		packboard.InjectScopeFull,
		false,
		true,
		time.Now().UTC(),
	)
	testutil.FailErr(t, "inject.RenderBoardOrientationInject failed", err)
	for _, want := range []string{inject.BoardOrientationInjectSentinel, packboard.PackBoardSentinel} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in block = %q", want, block)
		}
	}
}

func TestRenderBoardOrientationInject_WorkerSnapshotOmitsScanLegend(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	reader, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  profiles,
		RootCount: 1,
		WebSearch: true,
	}, prompts.TurnProjection{
		ProfileID: "implement",
		Declared:  true,
	})
	testutil.FailErr(t, "worker reader", err)
	block, err := inject.RenderBoardOrientationInject(
		context.Background(),
		renderer,
		reader,
		"sess-inject-test",
		api.BoardSnapshot{PackContentHash: "h1"},
		packboard.InjectScopeFull,
		true,
		false,
		time.Now().UTC(),
	)
	testutil.FailErr(t, "inject.RenderBoardOrientationInject failed", err)
	if strings.Contains(block, "scan_pack") {
		t.Fatalf("worker snapshot inject must omit scan legend: %q", block)
	}
}

func TestRenderBoardOrientationInject_MissingSentinel(t *testing.T) {
	engine := prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{})
	if err := engine.Register("inject/board-orientation.md", "## broken"); err != nil {
		testutil.FailErr(t, "engine.Register failed", err)
	}
	renderer := prompts.NewInjectRenderer(engine)
	reader := boardTestCoordReader(t)
	_, err := inject.RenderBoardOrientationInject(context.Background(), renderer, reader, "sess-inject-test", api.BoardSnapshot{}, packboard.InjectScopeFull, false, true, time.Now())
	if err == nil || !strings.Contains(err.Error(), inject.BoardOrientationInjectSentinel) {
		t.Fatalf("err = %v", err)
	}
}

func TestRenderBoardOrientationInject_PackBoardPairedWitnesses(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	plans, err := surface.CompileToolPlans(1)
	testutil.FailErr(t, "CompileToolPlans", err)

	var named []api.BoardWorker
	for range packboard.MaxOrientationWorkers + 2 {
		named = append(named, api.BoardWorker{Name: "w", AgentType: "implementer", State: api.WorkerStateWorking})
	}
	snap := api.BoardSnapshot{Workers: &api.BoardWorkersSlice{"workers": named}}
	now := time.Now().UTC()

	// Positive witness 1: pack_board offered directly.
	withBoard, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  profiles,
		RootCount: 1,
		WebSearch: true,
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_park",
		Plan:      plans["implement_park"],
		Offered:   (plans["implement_park"]).ImmediateNames(),
	})
	testutil.FailErr(t, "withBoard reader", err)
	pos, err := inject.RenderBoardOrientationInject(t.Context(), renderer, withBoard, "s", snap, packboard.InjectScopeFull, false, false, now)
	testutil.FailErr(t, "render with pack_board", err)
	if !strings.Contains(pos, "call `pack_board` for full pages") {
		t.Fatalf("positive witness missing pack_board call:\n%s", pos)
	}

	// Positive witness 2: pack_board deferred/loadable via request_tools (floor-only implement_investigate).
	floorInvestigate, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  profiles,
		RootCount: 1,
		WebSearch: true,
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Plan:      plans["implement_investigate"],
		Offered:   (plans["implement_investigate"]).ImmediateNames(),
	})
	testutil.FailErr(t, "floorInvestigate reader", err)
	loadable, err := inject.RenderBoardOrientationInject(t.Context(), renderer, floorInvestigate, "s", snap, packboard.InjectScopeFull, false, false, now)
	testutil.FailErr(t, "render loadable pack_board", err)
	if strings.Contains(loadable, "call `pack_board`") || !strings.Contains(loadable, `request_tools(need="pack_board")`) {
		t.Fatalf("loadable witness must teach request_tools(need=\"pack_board\"):\n%s", loadable)
	}

	// Negative witnesses: 0-root coordinator, implement_synthesis, and worker board inject omit pack_board.
	zeroRootPlans, err := surface.CompileToolPlans(0)
	testutil.FailErr(t, "CompileToolPlans(0)", err)
	zeroRoot, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  profiles,
		RootCount: 0,
		WebSearch: true,
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Plan:      zeroRootPlans["implement_investigate"],
		Offered:   (zeroRootPlans["implement_investigate"]).ImmediateNames(),
	})
	testutil.FailErr(t, "zeroRoot reader", err)
	synthesis, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  profiles,
		RootCount: 1,
		WebSearch: true,
	}, prompts.TurnProjection{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_synthesis",
		Plan:      plans["implement_synthesis"],
		Offered:   (plans["implement_synthesis"]).ImmediateNames(),
	})
	testutil.FailErr(t, "synthesis reader", err)
	worker, err := prompts.NewReader(prompts.HostFacts{
		Profiles:  profiles,
		RootCount: 1,
		WebSearch: true,
	}, prompts.TurnProjection{
		ProfileID: "implement",
		Offered:   []string{"command", "complete_leg"},
		Declared:  true,
	})
	testutil.FailErr(t, "worker reader", err)

	for label, r := range map[string]prompts.Reader{"0-root": zeroRoot, "implement_synthesis": synthesis} {
		out, err := inject.RenderBoardOrientationInject(t.Context(), renderer, r, "s", snap, packboard.InjectScopeFull, false, false, now)
		testutil.FailErr(t, "render "+label, err)
		if strings.Contains(out, "pack_board") {
			t.Fatalf("%s negative witness leaked pack_board:\n%s", label, out)
		}
		if !strings.Contains(out, "Workers omitted: 2 older workers.") {
			t.Fatalf("%s negative witness lost omitted workers count:\n%s", label, out)
		}
	}
	wOut, err := inject.RenderWorkerBoardInject(t.Context(), renderer, worker, "s", snap, packboard.InjectScopeFull, true, false, now)
	testutil.FailErr(t, "render worker board", err)
	if strings.Contains(wOut, "pack_board") {
		t.Fatalf("worker board negative witness leaked pack_board:\n%s", wOut)
	}
}
