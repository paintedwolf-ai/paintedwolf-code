package native_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
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

	tmpDir := t.TempDir()
	blueprintsDir := filepath.Join(tmpDir, filepath.FromSlash(settingsoverlay.Rel("blueprints")))
	if err := os.MkdirAll(blueprintsDir, 0o755); err != nil {
		testutil.FailErr(t, "mkdir blueprints", err)
	}
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: tmpDir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "plan_write_only"},
	}

	blueprint := settingsoverlay.Rel("blueprints/plan.md")
	_, err := writeTool.Run(t.Context(), map[string]any{
		"path": blueprint, "content": "# Plan\n",
	}, tctx)
	testutil.FailErr(t, "allowed blueprint write", err)
	body, err := os.ReadFile(filepath.Join(tmpDir, filepath.FromSlash(blueprint)))
	testutil.FailErr(t, "read blueprint write", err)
	if string(body) != "# Plan\n" {
		t.Fatalf("blueprint bytes = %q", body)
	}

	product := filepath.Join(tmpDir, "src", "main.py")
	testutil.FailErr(t, "create product directory", os.MkdirAll(filepath.Dir(product), 0o755))
	const original = "print('unchanged')\n"
	testutil.FailErr(t, "seed product file", os.WriteFile(product, []byte(original), 0o644))
	for _, tc := range []struct {
		tool string
		run  func() (string, error)
	}{
		{"write", func() (string, error) {
			return writeTool.Run(t.Context(), map[string]any{"path": "src/main.py", "content": "print('nope')\n"}, tctx)
		}},
		{"edit", func() (string, error) {
			return editTool.Run(t.Context(), map[string]any{"path": "src/main.py", "old_string": "unchanged", "new_string": "changed"}, tctx)
		}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			out, err := tc.run()
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != "WRITE_SCOPE_DENIED" {
				t.Fatalf("product mutation: result %q error %v, want WRITE_SCOPE_DENIED", out, err)
			}
			if reject.Data["tool"] != tc.tool || reject.Data["profile"] != "plan_write_only" || reject.Data["path"] != "src/main.py" {
				t.Fatalf("scope rejection facts = %+v", reject.Data)
			}
			if out != "" {
				t.Fatalf("denied mutation returned success output: %q", out)
			}
			body, err := os.ReadFile(product)
			testutil.FailErr(t, "read denied product path", err)
			if string(body) != original {
				t.Fatalf("denied mutation changed product bytes to %q", body)
			}
		})
	}
}
