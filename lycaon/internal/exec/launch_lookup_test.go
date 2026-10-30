//go:build darwin || linux

package exec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

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
