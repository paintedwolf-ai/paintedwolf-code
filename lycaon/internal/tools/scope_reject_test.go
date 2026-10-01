package tools

import (
	"context"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"strings"
	"testing"
)

func TestCoordinatorScopeRejectWrite(t *testing.T) {
	boundary := fixtureBoundary(t)
	profilePolicy := NewProfilePolicyEngine(boundary)
	reg := NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx ToolContext) (string, error) {
		path, _ := args["path"].(string)
		return "", boundary.AssertWriteScope(ctx, tctx.ActiveRootPath(), path, tctx.Agent)
	})
	exec := NewDefaultToolExecutor(profilePolicy, reg, "implement")

	// On investigate the coordinator may write product paths but not engine-internal
	// ones; an out-of-scope path still gets the investigate-denied observation.
	tctx := fixtureToolContext(t.TempDir())
	tctx.Agent = "coordinator"
	tctx.TurnSurfaceID = SurfaceImplementInvestigate
	_, err := exec.Invoke(context.Background(), "write", map[string]any{
		"path":    settingsoverlay.Rel("secrets.yaml"),
		"content": "x",
	}, tctx)
	if err == nil {
		t.Fatal("expected coordinator out-of-scope write to be blocked")
	}
	msg := err.Error()
	if !strings.Contains(msg, "COORDINATOR_INVESTIGATE_DENIED_PATH") {
		t.Fatalf("missing investigate deny code in reject:\n%s", msg)
	}
	if AsToolReject(err) == nil {
		t.Fatalf("expected ToolReject observation, got:\n%s", msg)
	}
}

// The promptloop surface check denies off-surface writes first; the executor's
// write scope still blocks a coordinator product write outside investigate.
func TestCoordinatorOrchestrateWriteBlockedByScope(t *testing.T) {
	boundary := fixtureBoundary(t)
	reg := NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx ToolContext) (string, error) {
		path, _ := args["path"].(string)
		return "", boundary.AssertWriteScope(ctx, tctx.ActiveRootPath(), path, tctx.Agent)
	})
	exec := NewDefaultToolExecutor(NewProfilePolicyEngine(boundary), reg, "implement")

	tctx := fixtureToolContext(t.TempDir())
	tctx.Agent = "coordinator"
	tctx.TurnSurfaceID = SurfaceImplementDispatch
	if _, err := exec.Invoke(context.Background(), "write", map[string]any{"path": "src/main.go", "content": "x"}, tctx); err == nil {
		t.Fatal("expected coordinator product write outside investigate to be blocked by scope")
	}
}

func TestCoordinatorProductReadInScope(t *testing.T) {
	boundary := fixtureBoundary(t)
	profilePolicy := NewProfilePolicyEngine(boundary)
	reg := NewDefaultRegistry()
	_ = reg.Register("read", func(ctx context.Context, args map[string]any, tctx ToolContext) (string, error) {
		path, _ := args["path"].(string)
		if err := boundary.AssertReadScope(ctx, tctx.ActiveRootPath(), path, tctx.Agent); err != nil {
			return "", err
		}
		return "ok", nil
	})
	exec := NewDefaultToolExecutor(profilePolicy, reg, "implement")

	tctx := fixtureToolContext(t.TempDir())
	tctx.Agent = "coordinator"
	out, err := exec.Invoke(context.Background(), "read", map[string]any{
		"path": "src/main.go",
	}, tctx)
	if err != nil {
		t.Fatalf("coordinator product read should be allowed: %v", err)
	}
	if out != "ok" {
		t.Fatalf("read out = %q", out)
	}
}
