package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func testInjectRenderer(t *testing.T) *prompts.InjectRenderer {
	t.Helper()
	return promptstest.InjectRenderer(t)
}

func TestRenderWorkerTaskPreambleWriteMode(t *testing.T) {
	ctx := context.Background()
	renderer := testInjectRenderer(t)
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "internal"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	block, err := RenderWorkerTaskPreamble(ctx, renderer, "sess-inject-test", projectDir,
		[]string{"internal/**", "lycaon/**"},
		&api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"src/game.js"}},
	)
	testutil.FailErr(t, "RenderWorkerTaskPreamble failed", err)
	for _, want := range []string{
		guidance.MarkerWorkerTaskPreamble,
		"Manifest touch paths",
		"Task mode",
		"mode: write",
		"src/game.js",
		"obligation:",
		"write, edit, replace_lines, or restore_version",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("missing %q in:\n%s", want, block)
		}
	}
	if strings.Contains(block, "lycaon/**") {
		t.Fatalf("lycaon touch path should be filtered for empty project:\n%s", block)
	}
}

func TestRenderWorkerTaskPreambleAbsentSuggestedPaths(t *testing.T) {
	ctx := context.Background()
	renderer := testInjectRenderer(t)
	projectDir := t.TempDir()
	block, err := RenderWorkerTaskPreamble(ctx, renderer, "sess-inject-test", projectDir,
		nil,
		&api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"Sources/GameEngine/**"}},
	)
	testutil.FailErr(t, "RenderWorkerTaskPreamble failed", err)
	if !strings.Contains(block, "new paths") || !strings.Contains(block, "don't exist yet") {
		t.Fatalf("expected create steer for absent suggestions:\n%s", block)
	}
}

func TestRenderWorkerTaskPreambleExistingSuggestedPathNoCreateSteer(t *testing.T) {
	ctx := context.Background()
	renderer := testInjectRenderer(t)
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "Sources", "GameEngine"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	block, err := RenderWorkerTaskPreamble(ctx, renderer, "sess-inject-test", projectDir,
		nil,
		&api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"Sources/GameEngine/**"}},
	)
	testutil.FailErr(t, "RenderWorkerTaskPreamble failed", err)
	if strings.Contains(block, "new paths") {
		t.Fatalf("existing suggestion should not get the create steer:\n%s", block)
	}
}

func TestRenderWorkerTaskPreambleReadMinimal(t *testing.T) {
	ctx := context.Background()
	renderer := testInjectRenderer(t)
	block, err := RenderWorkerTaskPreamble(ctx, renderer, "sess-inject-test", t.TempDir(), nil, &api.TaskScope{Mode: api.TaskScopeModeRead})
	testutil.FailErr(t, "RenderWorkerTaskPreamble failed", err)
	if block != "" {
		t.Fatalf("read mode without paths should be omitted: %q", block)
	}
}

func TestJoinWorkerAssignment(t *testing.T) {
	got := JoinWorkerAssignment("preamble", "assignment")
	if got != "preamble\n\nassignment" {
		t.Fatalf("got %q", got)
	}
}

func TestBuildWorkerPromptJoinsAssignment(t *testing.T) {
	ctx := context.Background()
	renderer := testInjectRenderer(t)
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, "internal", "auth"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	prompt, err := BuildWorkerPrompt(ctx, renderer, "sess-inject-test", projectDir,
		[]string{"internal/**"},
		&api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"internal/auth/**"}},
		"Implement login fix",
	)
	testutil.FailErr(t, "BuildWorkerPrompt failed", err)
	if !strings.Contains(prompt, "Manifest touch paths") {
		t.Fatalf("prompt = %q", prompt)
	}
	if !strings.Contains(prompt, "Task mode") {
		t.Fatalf("prompt = %q", prompt)
	}
	if !strings.HasSuffix(strings.TrimSpace(prompt), "Implement login fix") {
		t.Fatalf("prompt = %q", prompt)
	}
}
