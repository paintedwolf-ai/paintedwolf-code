package processes

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/userpath"
)

func TestResolvePathPublishesConfiguredExecutableSearch(t *testing.T) {
	preserveResolvedPath(t)
	first, second := t.TempDir(), t.TempDir()
	command := filepath.Join(second, "configured-command")
	testutil.FailErr(t, "create configured executable", os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0700))
	t.Setenv("LYCAON_COMMAND_PATH", first+string(filepath.ListSeparator)+second)
	t.Setenv("PATH", t.TempDir())
	runtime := &Runtime{}
	testutil.FailErr(t, "resolve configured command path", runtime.ResolvePath(t.Context()))
	if runtime.Path.Source() != userpath.SourceConfigured || runtime.Path.Failure() != userpath.FailureNone || !reflect.DeepEqual(runtime.Path.Entries(), []string{first, second}) {
		t.Fatalf("configured snapshot = %+v", runtime.Path)
	}
	found, err := execpkg.LookPath("configured-command")
	testutil.FailErr(t, "find command through published path", err)
	if found != command || execpkg.EffectivePathValue() != runtime.Path.Value() {
		t.Fatalf("executable search = %q, effective path = %q; want %q through %q", found, execpkg.EffectivePathValue(), command, runtime.Path.Value())
	}
}

func TestResolvePathRefusesInvalidConfigurationWithoutReplacingSnapshot(t *testing.T) {
	preserveResolvedPath(t)
	dir := t.TempDir()
	t.Setenv("LYCAON_COMMAND_PATH", dir)
	runtime := &Runtime{}
	testutil.FailErr(t, "publish initial configured path", runtime.ResolvePath(t.Context()))
	for _, invalid := range []string{"", "relative-bin", dir + string(filepath.ListSeparator) + "relative-bin"} {
		t.Setenv("LYCAON_COMMAND_PATH", invalid)
		if err := runtime.ResolvePath(t.Context()); err == nil {
			t.Fatalf("invalid configured path %q was accepted", invalid)
		}
		if runtime.Path.Value() != dir || execpkg.ResolvedPathValue() != dir {
			t.Fatalf("refused configuration replaced snapshot: runtime=%q, process=%q", runtime.Path.Value(), execpkg.ResolvedPathValue())
		}
	}
}

func TestResolvePathCanceledProbeRetainsLaunchEnvironment(t *testing.T) {
	preserveResolvedPath(t)
	unsetCommandPath(t)
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("SHELL", "/bin/sh")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	runtime := &Runtime{}
	testutil.FailErr(t, "resolve canceled login-shell probe", runtime.ResolvePath(ctx))
	if runtime.Path.Source() != userpath.SourceInherited || runtime.Path.Failure() == userpath.FailureNone || runtime.Path.Reason() == "" || runtime.Path.Value() != dir {
		t.Fatalf("canceled probe snapshot = %+v; want inherited path %q with structured failure", runtime.Path, dir)
	}
	if execpkg.EffectivePathValue() != dir || ctx.Err() != context.Canceled {
		t.Fatalf("canceled startup path = %q, context error = %v", execpkg.EffectivePathValue(), ctx.Err())
	}
}

func preserveResolvedPath(t *testing.T) {
	t.Helper()
	previous := execpkg.ResolvedPathValue()
	t.Cleanup(func() {
		if previous == "" {
			execpkg.SetResolvedPathSource(nil)
		} else {
			execpkg.SetResolvedPathSource(func() string { return previous })
		}
	})
}

func unsetCommandPath(t *testing.T) {
	t.Helper()
	value, present := os.LookupEnv("LYCAON_COMMAND_PATH")
	testutil.FailErr(t, "unset configured command path", os.Unsetenv("LYCAON_COMMAND_PATH"))
	t.Cleanup(func() {
		if present {
			testutil.FailErr(t, "restore configured command path", os.Setenv("LYCAON_COMMAND_PATH", value))
		}
	})
}
