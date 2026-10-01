package inject_test

import (
	"github.com/lycaon/lycaon/internal/coordinator/inject"

	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRenderBoardOrientationInject(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	block, err := inject.RenderBoardOrientationInject(
		context.Background(),
		renderer,
		"sess-inject-test",
		api.BoardSnapshot{PackContentHash: "h1"},
		packboard.InjectScopeFull,
		false,
		true,
		time.Now().UTC(),
	)
	testutil.FailErr(t, "inject.RenderBoardOrientationInject failed", err)
	for _, want := range []string{inject.BoardOrientationInjectSentinel, packboard.PackBoardSentinel, "Pack board"} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in block = %q", want, block)
		}
	}
}

func TestRenderBoardOrientationInject_WorkerSnapshotOmitsScanLegend(t *testing.T) {
	renderer := promptstest.InjectRenderer(t)
	block, err := inject.RenderBoardOrientationInject(
		context.Background(),
		renderer,
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
	_, err := inject.RenderBoardOrientationInject(context.Background(), renderer, "sess-inject-test", api.BoardSnapshot{}, packboard.InjectScopeFull, false, true, time.Now())
	if err == nil || !strings.Contains(err.Error(), inject.BoardOrientationInjectSentinel) {
		t.Fatalf("err = %v", err)
	}
}
