package inject_test

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/testutil"
)

func testConfigRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func TestImplementSpawnInjectNoFolderSearchDisabled(t *testing.T) {
	root := testConfigRoot(t)
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}))
	block, err := inject.RenderImplementSpawnInject(
		context.Background(),
		renderer,
		"sess-inject-test",
		nil,
		spawn.MaxInFlightTaskWorkers,
		spawn.SurfaceImplementRouting,
		0,
		false,
		false,
		nil, nil)
	testutil.FailErr(t, "RenderImplementSpawnInject", err)
	if strings.Contains(block, "web-researcher") {
		t.Fatalf("disabled web research must not mention web-researcher:\n%s", block)
	}
	if !strings.Contains(block, "attach a folder") {
		t.Fatalf("expected attach-a-folder guidance:\n%s", block)
	}
}

func TestImplementSpawnInjectNoFolderSearchEnabled(t *testing.T) {
	root := testConfigRoot(t)
	renderer := prompts.NewInjectRenderer(prompts.NewFileTemplateEngineLayers(prompts.PromptLayers{ModuleRoot: root}))
	block, err := inject.RenderImplementSpawnInject(
		context.Background(),
		renderer,
		"sess-inject-test",
		[]string{"web-researcher"},
		spawn.MaxInFlightTaskWorkers,
		spawn.SurfaceImplementRouting,
		0,
		false,
		true,
		nil, nil)
	testutil.FailErr(t, "RenderImplementSpawnInject", err)
	if !strings.Contains(block, "web-researcher") {
		t.Fatalf("enabled web research should document web-researcher:\n%s", block)
	}
}
