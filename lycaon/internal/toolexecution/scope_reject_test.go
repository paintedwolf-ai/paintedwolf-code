package toolexecution

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"strings"
	"testing"
)

func TestCoordinatorScopeRejectWrite(t *testing.T) {
	boundary := fixtureBoundary(t)
	profilePolicy := toolprofiles.NewProfilePolicyEngine(boundary)
	reg := tools.NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		path, _ := args["path"].(string)
		return "", boundary.AssertWriteScope(ctx, tctx.ActiveRootPath(), path, tctx.Identity.Agent)
	})
	exec := NewExecutor(profilePolicy, reg, "implement")

	// On investigate the coordinator may write product paths but not engine-internal
	// ones; an out-of-scope path still gets the investigate-denied observation.
	tctx := fixtureToolContext(t.TempDir())
	tctx.Identity.Agent = "coordinator"
	tctx.Turn.TurnSurfaceID = toolcontract.SurfaceImplementInvestigate
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
	if toolrejection.AsToolReject(err) == nil {
		t.Fatalf("expected ToolReject observation, got:\n%s", msg)
	}
}

// The promptloop surface check denies off-surface writes first; the executor's
// write scope still blocks a coordinator product write outside investigate.
func TestCoordinatorOrchestrateWriteBlockedByScope(t *testing.T) {
	boundary := fixtureBoundary(t)
	reg := tools.NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		path, _ := args["path"].(string)
		return "", boundary.AssertWriteScope(ctx, tctx.ActiveRootPath(), path, tctx.Identity.Agent)
	})
	exec := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), reg, "implement")

	tctx := fixtureToolContext(t.TempDir())
	tctx.Identity.Agent = "coordinator"
	tctx.Turn.TurnSurfaceID = toolcontract.SurfaceImplementDispatch
	if _, err := exec.Invoke(context.Background(), "write", map[string]any{"path": "src/main.go", "content": "x"}, tctx); err == nil {
		t.Fatal("expected coordinator product write outside investigate to be blocked by scope")
	}
}

func TestCoordinatorProductReadInScope(t *testing.T) {
	boundary := fixtureBoundary(t)
	profilePolicy := toolprofiles.NewProfilePolicyEngine(boundary)
	reg := tools.NewDefaultRegistry()
	_ = reg.Register("read", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		path, _ := args["path"].(string)
		if err := boundary.AssertReadScope(ctx, tctx.ActiveRootPath(), path, tctx.Identity.Agent); err != nil {
			return "", err
		}
		return "ok", nil
	})
	exec := NewExecutor(profilePolicy, reg, "implement")

	tctx := fixtureToolContext(t.TempDir())
	tctx.Identity.Agent = "coordinator"
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
