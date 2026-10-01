package native

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
)

// TestNativePathArgsRejectTraversal checks path rejection and error redaction.
func TestNativePathArgsRejectTraversal(t *testing.T) {
	projectDir := t.TempDir()
	boundary := nativefixture.Boundary(t)

	type tool interface {
		Name() string
		Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error)
	}

	allTools := []tool{
		&surveytools.ReadTool{Boundary: boundary},
		&WriteTool{Boundary: boundary},
		&EditTool{Boundary: boundary},
		&surveytools.FindTool{Boundary: boundary},
		&surveytools.GrepTool{Boundary: boundary},
		&surveytools.StatTool{Boundary: boundary},
		&surveytools.WcTool{Boundary: boundary},
		&surveytools.ListDirTool{Boundary: boundary},
		&ChmodTool{Boundary: boundary},
		&DeleteTool{Boundary: boundary},
	}

	type evil struct {
		label string
		path  string
	}
	evilPaths := []evil{
		{"parent escape", "../secret.txt"},
		{"deep parent escape", "../../../etc/passwd"},
		{"absolute outside", "/etc/passwd"},
		{"nested escape", "src/../../secret.txt"},
		{"null byte", "good.txt\x00../../etc/passwd"},
	}
	if runtime.GOOS == "windows" {
		evilPaths = append(evilPaths,
			evil{"windows drive", "C:\\Windows\\System32\\config\\SAM"},
		)
	}

	ctx := context.Background()

	for _, tl := range allTools {
		t.Run(tl.Name(), func(t *testing.T) {
			for _, e := range evilPaths {
				t.Run(e.label, func(t *testing.T) {
					args := map[string]any{"path": e.path}
					switch tl.Name() {
					case "write":
						args["content"] = "x"
					case "edit":
						args["old_string"] = "x"
						args["new_string"] = "y"
					case "grep":
						args["pattern"] = "x"
					case "stat", "wc", "delete":
						args = map[string]any{"paths": []any{e.path}}
					case "chmod":
						args = map[string]any{"paths": []any{e.path}, "mode": "+x"}
					}
					out, err := tl.Run(ctx, args, nativefixture.AgentContext(projectDir, "implement"))
					if err == nil {
						t.Fatalf("%s accepted evil path %q (out=%q) — boundary bypass?", tl.Name(), e.path, out)
					}
					// Errors must not expose the absolute project root.
					msg := err.Error()
					if strings.Contains(msg, projectDir) {
						t.Fatalf("%s error for %q leaks project_dir %q: %s",
							tl.Name(), e.path, projectDir, msg)
					}
				})
			}
		})
	}
}

func TestMultiRootResolveAbsNeverEscapesUnion(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "a")
	secondary := filepath.Join(base, "b")
	for _, dir := range []string{primary, secondary} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
	}
	roots := []projectroot.RootRef{
		{ID: "p", Label: "a", Path: primary, IsPrimary: true},
		{ID: "s", Label: "b", Path: secondary, IsPrimary: false},
	}
	evil := []string{"../outside.txt", "../../etc/passwd", "@missing/x", "../../../tmp"}
	for _, path := range evil {
		_, _, err := projectroot.ResolveAbs(roots, "p", path)
		if err == nil {
			t.Fatalf("ResolveAbs(%q) should fail", path)
		}
	}
	abs, _, err := projectroot.ResolveAbs(roots, "p", "@b/ok.txt")
	if err != nil {
		t.Fatalf("ResolveAbs labeled path: %v", err)
	}
	if !strings.HasPrefix(abs, secondary) {
		t.Fatalf("abs = %q want under secondary", abs)
	}
}
