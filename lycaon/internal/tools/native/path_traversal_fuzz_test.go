package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

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

// A refused native path names no absolute project root, for escapes spelled
// over a real tree whose outside target exists.
func TestNativePathRefusalsNameNoProjectRoot(t *testing.T) {
	base := t.TempDir()
	projectDir, outside := filepath.Join(base, "proj"), filepath.Join(base, "outside")
	for _, dir := range []string{projectDir, outside} {
		testutil.FailErr(t, "create directory", os.MkdirAll(dir, 0o755))
	}
	target := filepath.Join(outside, "secret.txt")
	testutil.FailErr(t, "write outside", os.WriteFile(target, []byte("outside"), 0o644))
	testutil.FailErr(t, "link outside", os.Symlink(outside, filepath.Join(projectDir, "link")))
	boundary := nativefixture.Boundary(t)
	type tool interface {
		Run(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error)
	}
	// The absolute target lies outside the project, scratch and every default write root.
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "read home directory", err)
	absOutside, err := os.MkdirTemp(home, ".pw-native-refusal-")
	testutil.FailErr(t, "create home folder", err)
	t.Cleanup(func() { _ = os.RemoveAll(absOutside) })
	absTarget := filepath.Join(absOutside, "secret.txt")
	testutil.FailErr(t, "write home target", os.WriteFile(absTarget, []byte("outside"), 0o644))
	for _, path := range []string{filepath.Join("..", "outside", "secret.txt"), filepath.Join("link", "secret.txt"), absTarget} {
		for name, run := range map[string]func() error{
			"write": func() error {
				_, err := (&WriteTool{Boundary: boundary}).Run(t.Context(), map[string]any{"path": path, "content": "x"}, nativefixture.AgentContext(projectDir, "implement"))
				return err
			},
			"chmod": func() error {
				_, err := tool(&ChmodTool{Boundary: boundary}).Run(t.Context(), map[string]any{"paths": []any{path}, "mode": "+x"}, nativefixture.AgentContext(projectDir, "implement"))
				return err
			},
			"delete": func() error {
				_, err := tool(&DeleteTool{Boundary: boundary}).Run(t.Context(), map[string]any{"paths": []any{path}}, nativefixture.AgentContext(projectDir, "implement"))
				return err
			},
		} {
			err := run()
			if err == nil {
				t.Errorf("%s accepted %q outside the project", name, path)
				continue
			}
			if strings.Contains(err.Error(), projectDir) {
				t.Errorf("%s refusal for %q names the project root: %v", name, path, err)
			}
		}
	}
	for _, file := range []string{target, absTarget} {
		if data, err := os.ReadFile(file); err != nil || string(data) != "outside" {
			t.Fatalf("outside file %s = %q, %v", file, data, err)
		}
	}
}
