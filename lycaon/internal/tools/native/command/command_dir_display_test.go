package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// resolvedTempDir returns a temp directory with symlinks resolved, so root
// matching compares the same spelling the host canonicalizes to.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp dir", err)
	return dir
}

func dirDisplayContext(t *testing.T) (tools.ToolContext, string, string, string, string) {
	t.Helper()
	app, lib, scratch, output := resolvedTempDir(t), resolvedTempDir(t), resolvedTempDir(t), resolvedTempDir(t)
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{
			Roots: []projectroot.RootRef{
				{ID: "app", Label: "app", Path: app, IsPrimary: true},
				{ID: "lib", Label: "lib", Path: lib},
			},
			ActiveRootID: "app",
		},
		Host: tools.InvocationHost{SessionScratchDir: scratch, SessionOutputDir: output},
	}
	return tctx, app, lib, scratch, output
}

func TestCommandDirDisplayUsesSessionFolderSpellings(t *testing.T) {
	tctx, app, lib, scratch, output := dirDisplayContext(t)
	outside := resolvedTempDir(t)
	for _, tc := range []struct {
		name, dir, want string
	}{
		{"empty", "", ""},
		{"relative passes through", "src", "src"},
		{"session folder", filepath.Join(app, "src"), "src"},
		{"session folder itself", app, "."},
		{"other attached folder", filepath.Join(lib, "pkg"), "@lib/pkg"},
		{"scratch area", filepath.Join(scratch, "build"), "@scratch/build"},
		{"scratch area itself", scratch, "@scratch"},
		{"output area", filepath.Join(output, "report"), "@output/report"},
		{"outside every folder", outside, outside},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandDirDisplay(tctx, tc.dir); got != tc.want {
				t.Fatalf("commandDirDisplay(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
}

func TestSessionAreaDisplayIgnoresUnsetAreas(t *testing.T) {
	tctx, app, _, _, _ := dirDisplayContext(t)
	if got := sessionAreaDisplay(tctx, app); got != "" {
		t.Fatalf("attached folder as session area = %q, want empty", got)
	}
	tctx.Host = tools.InvocationHost{}
	if got := sessionAreaDisplay(tctx, app); got != "" {
		t.Fatalf("no session areas = %q, want empty", got)
	}
	if got := sessionAreaDisplay(tctx, ""); got != "" {
		t.Fatalf("empty dir = %q, want empty", got)
	}
}

func TestRejectPwdEnvMismatchNamesBothDirectoriesInDisplayForm(t *testing.T) {
	tctx, app, lib, _, _ := dirDisplayContext(t)
	cwd := filepath.Join(app, "src")
	testutil.FailErr(t, "create cwd", os.MkdirAll(cwd, 0o755))

	for _, args := range []map[string]any{
		{},
		{"env": map[string]any{"HOME": "/home/agent"}},
		{"env": map[string]any{"PWD": ""}},
		{"env": map[string]any{"PWD": 7}},
		{"env": map[string]any{"PWD": cwd}},
		{"env": map[string]any{"PWD": "."}},
	} {
		if reject := rejectPwdEnvMismatch(tctx, args, "make", cwd, "src"); reject != nil {
			t.Fatalf("args %v rejected: %+v", args, reject)
		}
	}

	reject := rejectPwdEnvMismatch(tctx, map[string]any{"env": map[string]any{"PWD": filepath.Join(lib, "pkg")}}, "make", cwd, "src")
	if reject == nil || reject.Code != "COMMAND_PWD_NOT_CWD" {
		t.Fatalf("mismatched PWD = %+v, want COMMAND_PWD_NOT_CWD", reject)
	}
	if reject.Data["cwd"] != "src" || reject.Data["pwd"] != "@lib/pkg" || reject.Data["command"] != "make" {
		t.Fatalf("rejection data = %v, want cwd src, pwd @lib/pkg", reject.Data)
	}

	relative := rejectPwdEnvMismatch(tctx, map[string]any{"env": map[string]any{"PWD": "../other"}}, "make", cwd, "src")
	if relative == nil || relative.Data["pwd"] != "other" {
		t.Fatalf("relative PWD rejection = %+v, want pwd resolved against cwd as other", relative)
	}
}
