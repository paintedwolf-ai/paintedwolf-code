package contract

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func gitProgramContext(dir string) tools.ToolContext {
	return tools.ToolContext{
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}}, ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "explore_readonly"},
	}
}

func TestGitSurveysOutsideRepositoryKeepNativeFailure(t *testing.T) {
	for _, source := range []string{"git diff", "git status"} {
		t.Run(source, func(t *testing.T) {
			out, err := toolfixture.NewProgramFixture(t).Invoke(t, source, gitProgramContext(t.TempDir()))
			contractcheck.FailErr(t, "invoke outside repository", err)
			if out.Status == 0 || out.Refusal != nil || out.Stderr == "" {
				t.Fatalf("native repository failure lost: %+v", out)
			}
		})
	}
}

func TestGitStatusListsLargeTreeAndNarrowsPathsContract(t *testing.T) {
	dir := contractGitRepo(t)
	contractcheck.FailErr(t, "mkdir fixtures", os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755))
	const files = 150
	for i := range files {
		rel := filepath.Join("fixtures", fmt.Sprintf("f%03d.txt", i))
		contractcheck.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte("x\n"), 0o644))
	}
	contractcheck.FailErr(t, "write new source", os.WriteFile(filepath.Join(dir, "src", "new.go"), []byte("package src\n"), 0o644))
	f := toolfixture.NewProgramFixture(t)
	call := func(source string) command.ProgramOutcome {
		t.Helper()
		out, err := f.Invoke(t, source, gitProgramContext(dir))
		contractcheck.FailErr(t, "invoke git status", err)
		if out.Status != 0 || out.Refusal != nil || out.Truncated {
			t.Fatalf("incomplete status: %+v", out)
		}
		return out
	}
	out := call("git status --porcelain=v1 --untracked-files=all")
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(out.Stdout, "\n"), "\n") {
		if !strings.HasPrefix(line, "?? ") {
			t.Fatalf("unexpected status %q", line)
		}
		path := strings.TrimPrefix(line, "?? ")
		if seen[path] {
			t.Fatalf("duplicate path %q", path)
		}
		seen[path] = true
	}
	if len(seen) != files+1 || seen["fixtures/"] || !seen["fixtures/f000.txt"] || !seen["fixtures/f149.txt"] {
		t.Fatalf("status paths = %v", seen)
	}
	narrowed := call("git status --porcelain=v1 --untracked-files=all -- src")
	if narrowed.Stdout != "?? src/new.go\n" {
		t.Fatalf("path selection = %q", narrowed.Stdout)
	}
}

// contractGitRepo initialises a repository with one commit so diffs have a HEAD.
func contractGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	contractcheck.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	for i := range 12 {
		rel := filepath.Join("src", fmt.Sprintf("f%02d.go", i))
		contractcheck.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte("package src\n\nvar v = 0\n"), 0o644))
	}
	git("add", "-A")
	git("commit", "-m", "init")
	return dir
}

// Native Git retains complete hunks before the bounded command projection.
func TestGitDiffRetainsSelectedHunksThroughCommandContract(t *testing.T) {
	dir := contractGitRepo(t)
	body := "package src\n\n" + strings.Repeat("// a line of commentary that pads the hunk to a realistic width\n", 18) + "var v = 1\n"
	for i := range 12 {
		rel := filepath.Join("src", fmt.Sprintf("f%02d.go", i))
		contractcheck.FailErr(t, "modify "+rel, os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644))
	}
	f := toolfixture.NewProgramFixture(t)
	call := func(source string) command.ProgramOutcome {
		t.Helper()
		out, err := f.Invoke(t, source, gitProgramContext(dir))
		contractcheck.FailErr(t, "invoke git diff", err)
		if out.Status != 0 || out.Refusal != nil || out.Truncated {
			t.Fatalf("incomplete diff: %+v", out)
		}
		return out
	}
	out := call("git diff")
	if len(out.Stdout) <= 5<<10 || strings.Count(out.Stdout, "diff --git ") != 12 || strings.Count(out.Stdout, "+var v = 1") != 12 || out.StdoutRef == "" {
		t.Fatalf("diff lost retained hunks: %+v", out)
	}
	for i := range 12 {
		path := fmt.Sprintf("src/f%02d.go", i)
		if !strings.Contains(out.Stdout, "diff --git a/"+path+" b/"+path+"\n") {
			t.Fatalf("missing whole hunk %s", path)
		}
	}
	stat := call("git diff --stat")
	if !strings.Contains(stat.Stdout, "12 files changed") || strings.Contains(stat.Stdout, "diff --git ") {
		t.Fatalf("stat = %q", stat.Stdout)
	}
	selected := call("git diff HEAD -- src/f00.go")
	if strings.Count(selected.Stdout, "diff --git ") != 1 || !strings.Contains(selected.Stdout, "+var v = 1") {
		t.Fatalf("revision/path selection = %q", selected.Stdout)
	}
	refused, err := f.Invoke(t, "git diff --output=escaped.patch", gitProgramContext(dir))
	contractcheck.FailErr(t, "invoke unsupported output option", err)
	if refused.Refusal == nil || refused.Refusal.Code != "COMMAND_EFFECT_NOT_ALLOWED" {
		t.Fatalf("output option escaped capture: %+v", refused)
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.patch")); !os.IsNotExist(err) {
		t.Fatalf("refused output wrote a file: %v", err)
	}
}
