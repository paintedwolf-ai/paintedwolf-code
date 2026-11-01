//go:build darwin || linux

package exec

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLaunchLookupRetainsTheExecutorSelectedPath(t *testing.T) {
	if os.Getenv("PW_TEST_SELECTED_LAUNCH_PATH") != "1" {
		self, err := os.Executable()
		testutil.FailErr(t, "locate isolated lookup fixture", err)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := osexec.CommandContext(ctx, self, "-test.run=^TestLaunchLookupRetainsTheExecutorSelectedPath$")
		cmd.Env = append(os.Environ(), "PW_TEST_SELECTED_LAUNCH_PATH=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("executor-selected lookup fixture: %v\n%s", err, output)
		}
		return
	}
	// Isolate the process-wide platform checker from concurrently running tests.
	lookExecutable = func(candidate string) (string, error) { return candidate + ".exe", nil }
	cwd := t.TempDir()
	for _, tc := range []struct {
		name, path, want string
	}{
		{"tool", "/native/bin", "/native/bin/tool.exe"},
		{"tool", "relative", "relative/tool.exe"},
		{"tool", "", "tool.exe"},
		{"./tool", "", "./tool.exe"},
		{"/native/bin/tool", "", "/native/bin/tool.exe"},
	} {
		got, err := LaunchLookup(tc.name, ExecOpts{Dir: cwd, Env: []string{"PATH=" + tc.path}})
		testutil.FailErr(t, "select executor-qualified path", err)
		if got != tc.want {
			t.Fatalf("lookup %q on PATH %q = %q, want %q", tc.name, tc.path, got, tc.want)
		}
	}
}

// LaunchLookup selects exactly what prepareNativeCommand would: the final
// child PATH from the launch environment and the launch directory.
func TestLaunchLookupSelectsTheLaunchProgram(t *testing.T) {
	first, second, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	for _, path := range []string{filepath.Join(first, "tool"), filepath.Join(second, "tool"), filepath.Join(second, "extra"), filepath.Join(cwd, "local")} {
		testutil.FailErr(t, "write "+path, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	}
	testutil.FailErr(t, "write plain", os.WriteFile(filepath.Join(first, "plain"), nil, 0o644))
	base := ExecOpts{Dir: cwd, Env: []string{"PATH=" + first + string(os.PathListSeparator) + second}}
	withExtra := base
	withExtra.Env = []string{"PATH=" + first}
	withExtra.PathExtra = []string{second}
	cases := []struct {
		name string
		opts ExecOpts
		want string
	}{
		{"tool", base, filepath.Join(first, "tool")},
		{"extra", withExtra, filepath.Join(second, "extra")},
		{"./local", base, "./local"},
	}
	for _, c := range cases {
		got, err := LaunchLookup(c.name, c.opts)
		testutil.FailErr(t, "lookup "+c.name, err)
		final, launchCwd, err := launchEnvironment(c.opts, nil, false)
		testutil.FailErr(t, "launch environment", err)
		launch, err := resolveLaunchEntry(c.name, launchCwd, final.Env)
		testutil.FailErr(t, "launch selection", err)
		if got != c.want || got != launch {
			t.Fatalf("%s: lookup %q, launch %q, want %q", c.name, got, launch, c.want)
		}
	}
	var missing *confine.CommandNotFoundError
	for _, name := range []string{"plain", "absent"} {
		if _, err := LaunchLookup(name, base); !errors.As(err, &missing) {
			t.Fatalf("%s: err = %v, want command not found", name, err)
		}
	}
	reserved := base
	reserved.InlineEnv = map[string]string{"PATH": first}
	if _, err := LaunchLookup("tool", reserved); !errors.Is(err, ErrBlockedEnvKey) {
		t.Fatalf("inline PATH: err = %v", err)
	}
	relative := base
	relative.Dir = "relative"
	if _, err := LaunchLookup("tool", relative); err == nil {
		t.Fatal("lookup without an absolute launch directory accepted")
	}
}
