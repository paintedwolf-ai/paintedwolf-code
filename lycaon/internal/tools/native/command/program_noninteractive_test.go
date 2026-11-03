//go:build darwin || linux

package command_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func requiredExternal(t *testing.T, name string) string {
	t.Helper()
	binary, err := exec.LookPath(name)
	testutil.FailErr(t, "required external fixture "+name, err)
	return binary
}

func shellArgument(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func externalProgram(binary string, args ...string) string {
	words := []string{"external", "--", shellArgument(binary)}
	for _, arg := range args {
		words = append(words, shellArgument(arg))
	}
	return strings.Join(words, " ")
}

// Compare against the real utility under the shared process environment. No
// expected editor or hook error is synthesized by the command owner.
func TestProgramGitPreservesOwnEditorAndHookFailures(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	f := newProgramFixture(t)
	git := requiredExternal(t, "git")
	opts, err := lycexec.AgentLaunchOptions(lycexec.HostLaunch("noninteractive fixture"), f.Dir, nil, nil)
	testutil.FailErr(t, "build process environment", err)
	environment, err := lycexec.ProcessEnvironment(opts)
	testutil.FailErr(t, "resolve process environment", err)
	base := []string{"-c", "user.name=Fixture", "-c", "user.email=fixture", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=.git/hooks"}
	runGit := func(args ...string) (string, string, int) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), git, append(append([]string{}, base...), args...)...)
		cmd.Dir, cmd.Env = f.Dir, environment
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		status := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("launch Git fixture: %v", err)
			}
			status = exit.ExitCode()
		}
		return stdout.String(), stderr.String(), status
	}
	mustGit := func(args ...string) string {
		t.Helper()
		stdout, stderr, status := runGit(args...)
		if status != 0 {
			t.Fatalf("Git setup %v: status=%d stderr=%q", args, status, stderr)
		}
		return stdout
	}
	runNative := func(args ...string) string {
		t.Helper()
		out, err := f.call(t, t.Context(), externalProgram(git, append(append([]string{}, base...), args...)...))
		testutil.FailErr(t, "run Git through command entry", err)
		return out
	}
	mustGit("init", "-q", "--template="+t.TempDir())
	testutil.FailErr(t, "seed staged file", os.WriteFile(filepath.Join(f.Dir, "note"), []byte("before\n"), 0o600))
	mustGit("add", "note")
	_, editorError, editorStatus := runGit("commit")
	if editorStatus == 0 || editorError == "" {
		t.Fatal("Git fixture unexpectedly committed without an editor")
	}
	out := runNative("commit")
	got := decodeOutcome(t, out)
	if got.Status != editorStatus || got.Stderr != editorError {
		t.Fatalf("Git error changed: status=%d stderr=%q; want %d %q", got.Status, got.Stderr, editorStatus, editorError)
	}
	if _, _, status := runGit("rev-parse", "--verify", "HEAD"); status == 0 {
		t.Fatal("failed commit created history")
	}
	out = runNative("commit", "-m", "explicit message")
	if got := decodeOutcome(t, out); got.Status != 0 {
		t.Fatalf("explicit commit failed: %+v", got)
	}
	head := mustGit("rev-parse", "HEAD")
	if subject := mustGit("log", "-1", "--format=%s"); subject != "explicit message\n" {
		t.Fatalf("commit subject=%q", subject)
	}
	testutil.FailErr(t, "change staged file", os.WriteFile(filepath.Join(f.Dir, "note"), []byte("after\n"), 0o600))
	mustGit("add", "note")
	testutil.FailErr(t, "create hooks directory", os.MkdirAll(filepath.Join(f.Dir, ".git", "hooks"), 0o700))
	testutil.FailErr(t, "install rejecting hook", os.WriteFile(filepath.Join(f.Dir, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\nprintf '%s\\n' 'hook declined' >&2\nexit 9\n"), 0o700))
	_, hookError, hookStatus := runGit("commit")
	if hookStatus == 0 || hookError == "" {
		t.Fatal("rejecting hook did not reject commit")
	}
	out = runNative("commit")
	got = decodeOutcome(t, out)
	if got.Status != hookStatus || got.Stderr != hookError {
		t.Fatalf("hook error changed: status=%d stderr=%q; want %d %q", got.Status, got.Stderr, hookStatus, hookError)
	}
	if current := mustGit("rev-parse", "HEAD"); current != head {
		t.Fatal("rejecting hook changed commit history")
	}
}

func TestProgramPythonReceivesEOFWithoutControllingTerminal(t *testing.T) {
	f := newProgramFixture(t)
	binary := requiredExternal(t, "python3")
	source := "import os,sys\nprint(int(sys.stdin.isatty()))\ntry:\n fd=os.open('/dev/tty',os.O_RDONLY)\nexcept OSError as e:\n print(e.errno)\nelse:\n os.close(fd)\n raise SystemExit(99)\ninput()"
	out, err := f.call(t, t.Context(), externalProgram(binary, "-c", source))
	testutil.FailErr(t, "read interactive input", err)
	got := decodeOutcome(t, out)
	if got.Status != 1 || got.Stdout != fmt.Sprintf("0\n%d\n", syscall.ENXIO) || !strings.Contains(got.Stderr, "EOFError") {
		t.Fatalf("Python did not receive EOF without a terminal: %+v", got)
	}
}

func TestProgramNpmInitSettlesOnEOF(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	f := newProgramFixture(t)
	binary := requiredExternal(t, "npm")
	out, err := f.call(t, t.Context(), externalProgram(binary, "init"))
	testutil.FailErr(t, "run npm init", err)
	got := decodeOutcome(t, out)
	if got.Status != 1 {
		t.Fatalf("npm did not settle on EOF: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(f.Dir, "package.json")); !os.IsNotExist(err) {
		t.Fatalf("abandoned init published a package: %v", err)
	}
}
