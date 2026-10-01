package contract

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/tools"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func loadBundledCoordinatorProfile(t *testing.T) sandbox.ToolProfile {
	t.Helper()
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	for _, p := range profiles {
		if p.ID == "coordinator" {
			return p
		}
	}
	t.Fatal("coordinator profile missing")
	return sandbox.ToolProfile{}
}

func TestCoordinatorProfileAllowsGlobGrep(t *testing.T) {
	t.Parallel()
	prof := loadBundledCoordinatorProfile(t)
	ctx := context.Background()
	b := loadBundledSandboxBoundary(t)

	for _, tool := range []string{"find", "grep"} {
		if !prof.Tools[tool] {
			t.Fatalf("coordinator profile must allow %q", tool)
		}
		if err := b.AssertToolAllowed(ctx, "coordinator", tool, sandbox.ToolAccessProfile); err != nil {
			t.Fatalf("coordinator must allow %q at profile boundary: %v", tool, err)
		}
	}
}

func TestCoordinatorProfileReadWriteAllowlists(t *testing.T) {
	t.Parallel()
	prof := loadBundledCoordinatorProfile(t)
	scopes, err := sandbox.LoadPathScopes()
	contractcheck.FailErr(t, "load path-scopes.yaml", err)
	readWant := scopes["coordinator_product_read"]
	writeWant := append([]string(nil), scopes["coordinator_orchestration"].Write...)
	if !slices.Equal(prof.ReadGlobs, readWant.Read) {
		t.Fatalf("read_globs = %v want %v", prof.ReadGlobs, readWant.Read)
	}
	if !slices.Equal(prof.WriteGlobs, writeWant) {
		t.Fatalf("write_globs = %v want %v", prof.WriteGlobs, writeWant)
	}
}

func TestCoordinatorProfileToolsIncludeSurveyRead(t *testing.T) {
	exec := toolfixture.ContractToolExecutor(t)
	names := toolfixture.SortedToolNames(context.Background(), exec, "coordinator")
	// Profile grant; the turn surface gates invoke.
	for _, want := range []string{"find", "grep", "read", "command"} {
		if !slices.Contains(names, want) {
			t.Fatalf("coordinator profile schema must include %q; got %v", want, names)
		}
	}
}

func TestCoordinatorReadProductPathAllowed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		contractcheck.FailErr(t, "create directory", err)
	}
	srcPath := filepath.Join(srcDir, "main.go")
	if err := os.WriteFile(srcPath, []byte("package main\n"), 0o644); err != nil {
		contractcheck.FailErr(t, "write file", err)
	}

	b := loadBundledSandboxBoundary(t)
	read := &surveytools.ReadTool{Boundary: b}
	out, err := read.Run(context.Background(), map[string]any{
		"path": "src/main.go",
	}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "coordinator",
	})
	if err != nil {
		t.Fatalf("coordinator read product path: %v", err)
	}
	if !strings.Contains(out, "package main") {
		t.Fatalf("unexpected read output: %q", out)
	}
	if !strings.Contains(out, `"receipt"`) {
		t.Fatalf("expected survey receipt in output: %q", out)
	}
}

func TestCoordinatorReadPlanPathAllowed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	planDir := filepath.Join(root, settingsoverlay.DirName(), "blueprints")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		contractcheck.FailErr(t, "create directory", err)
	}
	planPath := filepath.Join(planDir, "game.md")
	if err := os.WriteFile(planPath, []byte("# plan"), 0o644); err != nil {
		contractcheck.FailErr(t, "write file", err)
	}

	b := loadBundledSandboxBoundary(t)
	read := &surveytools.ReadTool{Boundary: b}
	out, err := read.Run(context.Background(), map[string]any{
		"path": settingsoverlay.Rel("blueprints/game.md"),
	}, tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: root, IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "coordinator",
	})
	if err != nil {
		t.Fatalf("coordinator read plan path: %v", err)
	}
	if !strings.Contains(out, "plan") {
		t.Fatalf("unexpected read output: %q", out)
	}
}

func loadBundledSandboxBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	cfg, err := sandbox.LoadConfig()
	contractcheck.FailErr(t, "load sandbox config", err)
	profiles, err := sandbox.LoadToolProfiles()
	contractcheck.FailErr(t, "load tool profiles", err)
	return sandbox.NewBoundary(cfg, profiles)
}
