package native_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func testBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	cfg, err := sandbox.LoadConfig()
	testutil.FailErr(t, "LoadConfig", err)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	return sandbox.NewBoundary(cfg, profiles)
}

func TestWriteToolPlanWriterScopeAllowedAndDenied(t *testing.T) {
	boundary := testBoundary(t)
	writeTool := &native.WriteTool{Boundary: boundary}
	editTool := &native.EditTool{Boundary: boundary}
	policy := toolprofiles.NewProfilePolicyEngine(boundary)
	reg := tools.NewDefaultRegistry()
	_ = reg.Register("write", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return writeTool.Run(ctx, args, tctx)
	})
	_ = reg.Register("edit", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return editTool.Run(ctx, args, tctx)
	})
	exec := toolexecution.NewExecutor(policy, reg, toolprofiles.DefaultToolProfileID)

	tmpDir := t.TempDir()
	blueprintsDir := filepath.Join(tmpDir, filepath.FromSlash(settingsoverlay.Rel("blueprints")))
	if err := os.MkdirAll(blueprintsDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir blueprints", err)
	}
	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmpDir, IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "plan_write_only",
	}

	_, err := exec.Invoke(context.Background(), "write", map[string]any{
		"path":    settingsoverlay.Rel("blueprints/plan.md"),
		"content": "# Plan\n",
	}, tctx)
	testutil.FailErr(t, "allowed blueprint write", err)

	_, err = exec.Invoke(context.Background(), "write", map[string]any{
		"path":    "src/main.py",
		"content": "print('nope')\n",
	}, tctx)
	if err == nil {
		t.Fatal("expected product path write to be denied")
	}
	if !strings.Contains(err.Error(), "WRITE_SCOPE_DENIED") {
		t.Fatalf("write err = %v want WRITE_SCOPE_DENIED", err)
	}

	_, err = exec.Invoke(context.Background(), "edit", map[string]any{
		"path":       "src/main.py",
		"old_string": "x",
		"new_string": "y",
	}, tctx)
	if err == nil {
		t.Fatal("expected product path edit to be denied")
	}
	if !strings.Contains(err.Error(), "WRITE_SCOPE_DENIED") {
		t.Fatalf("edit err = %v want WRITE_SCOPE_DENIED", err)
	}
	tr := toolrejection.AsToolReject(err)
	if tr == nil {
		t.Fatalf("edit reject should be ToolReject: %v", err)
	}
	if tool, _ := tr.Data["tool"].(string); tool != "edit" {
		t.Fatalf("edit reject Data.tool = %v want edit", tr.Data["tool"])
	}
}
