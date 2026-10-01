package exec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func pathOf(t *testing.T, env []string) string {
	t.Helper()
	for _, entry := range env {
		if key, value, ok := strings.Cut(entry, "="); ok && key == "PATH" {
			return value
		}
	}
	t.Fatal("no PATH in environment")
	return ""
}

// Appended resource paths cannot shadow the configured command path.
func TestPathExtraCannotShadowAnInstalledProgram(t *testing.T) {
	env, err := buildProcessEnv([]string{"PATH=/opt/homebrew/bin:/usr/bin"}, nil, []string{"/Applications/Vendor.app/Contents/Resources/bin"})
	testutil.FailErr(t, "buildProcessEnv", err)

	got := filepath.SplitList(pathOf(t, env))
	if len(got) != 3 || got[0] != "/opt/homebrew/bin" {
		t.Fatalf("PATH = %v, want the person's own directories first", got)
	}
	if got[2] != "/Applications/Vendor.app/Contents/Resources/bin" {
		t.Fatalf("PATH = %v, want the vendor directory last", got)
	}
}

func TestPathExtraSkipsDirectoriesAlreadyPresent(t *testing.T) {
	env, err := buildProcessEnv([]string{"PATH=/usr/bin:/bin"}, nil, []string{"/usr/bin/", "/usr/bin"})
	testutil.FailErr(t, "buildProcessEnv", err)

	if got := pathOf(t, env); got != "/usr/bin:/bin" {
		t.Fatalf("PATH = %q, want no duplicate entry", got)
	}
}

func TestPathExtraRejectsEntriesThatCannotNameAProgram(t *testing.T) {
	env, err := buildProcessEnv([]string{"PATH=/usr/bin"}, nil, []string{"", ".", "relative/dir"})
	testutil.FailErr(t, "buildProcessEnv", err)

	if got := pathOf(t, env); got != "/usr/bin" {
		t.Fatalf("PATH = %q, want empty, current-directory and relative entries dropped", got)
	}
}

func TestPathExtraAppliesWhenTheEnvironmentHasNoPath(t *testing.T) {
	env, err := buildProcessEnv([]string{"HOME=/tmp"}, nil, []string{"/opt/vendor/bin"})
	testutil.FailErr(t, "buildProcessEnv", err)

	if got := pathOf(t, env); got != "/opt/vendor/bin" {
		t.Fatalf("PATH = %q, want the contributed directory to stand alone", got)
	}
}

// An explicit Env is caller intent. Only "inherit the environment" is redefined by the
// resolved user PATH, so a caller that states a PATH keeps it.
func TestResolvedPathDoesNotOverrideAnExplicitEnv(t *testing.T) {
	SetResolvedPathSource(func() string { return "/resolved/bin" })
	t.Cleanup(func() { SetResolvedPathSource(nil) })

	env, err := buildProcessEnv([]string{"PATH=/explicit/bin"}, nil, nil)
	testutil.FailErr(t, "buildProcessEnv", err)

	if got := pathOf(t, env); got != "/explicit/bin" {
		t.Fatalf("PATH = %q, want the caller's explicit value", got)
	}
}

func TestInheritedEnvironUsesTheResolvedPath(t *testing.T) {
	SetResolvedPathSource(func() string { return "/resolved/bin:/usr/bin" })
	t.Cleanup(func() { SetResolvedPathSource(nil) })

	if got := pathOf(t, InheritedEnviron()); got != "/resolved/bin:/usr/bin" {
		t.Fatalf("PATH = %q, want the resolved user PATH", got)
	}
}

// Nothing wired means leave the inherited PATH alone. A test binary or an early-startup
// call has no business losing its PATH because wiring has not run.
func TestInheritedEnvironIsUnchangedWithoutAResolvedPath(t *testing.T) {
	SetResolvedPathSource(nil)
	if got := pathOf(t, InheritedEnviron()); got == "" {
		t.Fatal("PATH is empty with no resolved source wired")
	}
}

// A bare name resolves on the resolved PATH, not the engine's process PATH; a
// name with a separator is checked as written.
func TestLookPathSearchesTheResolvedPath(t *testing.T) {
	resolved := t.TempDir()
	tool := filepath.Join(resolved, "resolved-only-tool")
	testutil.FailErr(t, "write tool", os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", t.TempDir())
	SetResolvedPathSource(func() string { return resolved })
	t.Cleanup(func() { SetResolvedPathSource(nil) })

	if got, err := LookPath("resolved-only-tool"); err != nil || got != tool {
		t.Fatalf("LookPath(bare) = %q, %v; want %q", got, err, tool)
	}
	if got, err := LookPath(tool); err != nil || got != tool {
		t.Fatalf("LookPath(path) = %q, %v; want %q", got, err, tool)
	}
	if _, err := LookPath("absent-tool"); !errors.Is(err, ErrNotInPath) {
		t.Fatalf("LookPath(absent) err = %v, want ErrNotInPath", err)
	}
}
