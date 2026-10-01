package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCoordinatorGuardStructuredReject(t *testing.T) {
	boundary := fixtureBoundary(t)
	profilePolicy := NewProfilePolicyEngine(boundary)
	guard := NewGuidanceRejectPolicy(profilePolicy)
	reg := NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx ToolContext) (string, error) {
		path, _ := args["path"].(string)
		if path == "" {
			return "", fmt.Errorf("missing path")
		}
		if err := assertProfileWriteScopeForTest(ctx, boundary, tctx, path); err != nil {
			return "", err
		}
		return "ok", nil
	})
	exec := NewDefaultToolExecutor(guard, reg, "implement")

	root := t.TempDir()
	tctx := fixtureToolContext(root)
	tctx.Agent = "coordinator"
	tctx.TurnSurfaceID = SurfaceImplementDispatch
	// The write scope blocks off-investigate product writes; orchestrate-write
	// guidance comes from the promptloop surface check.
	_, err := exec.Invoke(context.Background(), "write", map[string]any{"path": "main.go", "content": "x"}, tctx)
	if err == nil {
		t.Fatal("expected coordinator write to be blocked")
	}
}

func TestCoordinatorInvestigateProductWriteAllowed(t *testing.T) {
	boundary := fixtureBoundary(t)
	profilePolicy := NewProfilePolicyEngine(boundary)
	reg := NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx ToolContext) (string, error) {
		path, _ := args["path"].(string)
		if err := assertProfileWriteScopeForTest(ctx, boundary, tctx, path); err != nil {
			return "", err
		}
		return "ok", nil
	})
	exec := NewDefaultToolExecutor(profilePolicy, reg, "implement")

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	tctx := fixtureToolContext(root)
	tctx.Agent = "coordinator"
	tctx.TurnSurfaceID = SurfaceImplementInvestigate
	out, err := exec.Invoke(context.Background(), "write", map[string]any{
		"path":    "src/foo.go",
		"content": "package foo",
	}, tctx)
	if err != nil {
		t.Fatalf("investigate product write: %v", err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
}

// Investigate write scope allows product paths, including dependency trees, and
// project overlay paths; overlay writes reach agent-policy approval.
func TestCoordinatorInvestigateAllowsProductAndOverlayWrites(t *testing.T) {
	boundary := fixtureBoundary(t)
	profilePolicy := NewProfilePolicyEngine(boundary)
	reg := NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx ToolContext) (string, error) {
		path, _ := args["path"].(string)
		return "", boundary.AssertNamedWriteScope(ctx, tctx.ActiveRootPath(), path, CoordinatorProductWriteScope)
	})
	exec := NewDefaultToolExecutor(profilePolicy, reg, "implement")

	tctx := fixtureToolContext(t.TempDir())
	tctx.Agent = "coordinator"
	tctx.TurnSurfaceID = SurfaceImplementInvestigate
	for _, p := range []string{
		"node_modules/pkg/index.js",
		"vendor/pkg/foo.go",
		"src/app.go",
		settingsoverlay.Rel("rules/build.yaml"),
		settingsoverlay.Rel("ignores.yaml"),
	} {
		if _, err := exec.Invoke(context.Background(), "write", map[string]any{
			"path":    p,
			"content": "x",
		}, tctx); err != nil {
			t.Fatalf("path %q must not be denied by investigate write scope: %v", p, err)
		}
	}
}

func TestCoordinatorInvestigateAllowsPlanNotes(t *testing.T) {
	boundary := fixtureBoundary(t)
	profilePolicy := NewProfilePolicyEngine(boundary)
	reg := NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx ToolContext) (string, error) {
		path, _ := args["path"].(string)
		if err := assertProfileWriteScopeForTest(ctx, boundary, tctx, path); err != nil {
			return "", err
		}
		return "ok", nil
	})
	exec := NewDefaultToolExecutor(profilePolicy, reg, "implement")

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, settingsoverlay.DirName(), "blueprints"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	tctx := fixtureToolContext(root)
	tctx.Agent = "coordinator"
	tctx.TurnSurfaceID = SurfaceImplementInvestigate
	out, err := exec.Invoke(context.Background(), "write", map[string]any{
		"path":    settingsoverlay.Rel("blueprints/foo.md"),
		"content": "# notes",
	}, tctx)
	if err != nil {
		t.Fatalf("plan note write: %v", err)
	}
	if out != "ok" {
		t.Fatalf("out = %q", out)
	}
}

func TestCoordinatorInvestigateRejectsShadowBoardPath(t *testing.T) {
	boundary := fixtureBoundary(t)
	profilePolicy := NewProfilePolicyEngine(boundary)
	reg := NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx ToolContext) (string, error) {
		path, _ := args["path"].(string)
		if err := assertProfileWriteScopeForTest(ctx, boundary, tctx, path); err != nil {
			return "", err
		}
		return "ok", nil
	})
	exec := NewDefaultToolExecutor(profilePolicy, reg, "implement")

	for _, path := range []string{settingsoverlay.Rel("plans/board.md"), settingsoverlay.Rel("plans/board.plan.md")} {
		tctx := fixtureToolContext(t.TempDir())
		tctx.Agent = "coordinator"
		tctx.TurnSurfaceID = SurfaceImplementInvestigate
		_, err := exec.Invoke(context.Background(), "write", map[string]any{
			"path":    path,
			"content": "# shadow board",
		}, tctx)
		if err == nil {
			t.Fatalf("expected shadow board reject for %q", path)
		}
		if !strings.Contains(err.Error(), "COORDINATOR_PROGRESS_SHADOW_BOARD") {
			t.Fatalf("path %q: expected shadow board code, got: %v", path, err)
		}
	}
}

func assertProfileWriteScopeForTest(ctx context.Context, boundary *sandbox.Boundary, tctx ToolContext, path string) error {
	if tctx.Agent == "coordinator" && tctx.TurnSurfaceID == SurfaceImplementInvestigate {
		if progress.IsShadowBoardPath(path) {
			return &ToolReject{
				Code: "COORDINATOR_PROGRESS_SHADOW_BOARD",
				Data: map[string]any{
					"path": path,
					"tool": "write",
				},
			}
		}
		return boundary.AssertNamedWriteScope(sandbox.WithSessionID(ctx, tctx.SessionID), tctx.ActiveRootPath(), path, CoordinatorProductWriteScope)
	}
	profile := tctx.Agent
	if profile == "" {
		profile = "implement"
	}
	return boundary.AssertWriteScope(ctx, tctx.ActiveRootPath(), path, profile)
}
