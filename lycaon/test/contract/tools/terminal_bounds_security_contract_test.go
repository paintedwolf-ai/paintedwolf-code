package contract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestTerminalOpenRequiresProjectRoot keeps the working directory session-scoped.
func TestTerminalOpenRequiresProjectRoot(t *testing.T) {
	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	reg := tools.NewDefaultRegistry()
	contractcheck.FailErr(t, "register", native.RegisterTerminalSessionTools(reg, bg))
	_, err := reg.Run(context.Background(), "terminal_open", map[string]any{
		"command": "true",
	}, tools.ToolContext{SessionID: "s", ProjectID: "p"})
	if err == nil {
		t.Fatal("expected PROJECT_HAS_NO_ROOTS reject")
	}
	var rej *tools.ToolReject
	if !errors.As(err, &rej) || rej.Code != "PROJECT_HAS_NO_ROOTS" {
		t.Fatalf("err = %v, want PROJECT_HAS_NO_ROOTS", err)
	}
	if rej.ArgumentValidation {
		t.Fatal("missing project roots must not report invalid tool arguments")
	}
}

// TestTerminalPTYReusesBoundedExecGuards keeps both process paths on shared guards.
func TestTerminalPTYReusesBoundedExecGuards(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	checks := []struct {
		path    string
		markers []string
	}{
		{
			path: filepath.Join(root, "lycaon", "internal", "exec", "pty_start.go"),
			markers: []string{
				"toExecOpts",
				"buildExecCmd",
				"configureCmdEnv",
			},
		},
		{
			path: filepath.Join(root, "lycaon", "internal", "exec", "sanitize_env.go"),
			markers: []string{
				"SanitizeEnviron",
				"InheritedEnviron",
			},
		},
		{
			path: filepath.Join(root, "lycaon", "internal", "bgprocess", "pty.go"),
			markers: []string{
				"StartPTY",
				"MaxOutputBytes",
				"RingBufferBytes",
			},
		},
		{
			path: filepath.Join(root, "lycaon", "internal", "exec", "pty_windows.go"),
			markers: []string{
				"ErrPTYUnsupported",
			},
		},
		{
			path: filepath.Join(root, "lycaon", "internal", "tools", "native", "terminal", "session.go"),
			markers: []string{
				"TERMINAL_UNSUPPORTED",
				"ErrPTYUnsupported",
				"pty_unsupported",
			},
		},
		{
			path: filepath.Join(root, "lycaon", "internal", "tools", "native", "terminal", "session_open.go"),
			markers: []string{
				"DefaultConfinement",
				"BindAction",
				"OnExit",
				"AgentLaunch",
			},
		},
	}
	for _, tc := range checks {
		data, err := os.ReadFile(tc.path)
		contractcheck.FailErr(t, "read "+tc.path, err)
		text := string(data)
		for _, marker := range tc.markers {
			if !strings.Contains(text, marker) {
				t.Fatalf("%s missing %q", tc.path, marker)
			}
		}
	}
}

func TestTerminalUnsupportedMapsOnWindowsCI(t *testing.T) {
	if runtime.GOOS != "windows" {
		// Runtime support is exercised on its native builder.
		t.Skip("runtime unsupported path asserted on windows builders")
	}
	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	reg := tools.NewDefaultRegistry()
	contractcheck.FailErr(t, "register", native.RegisterTerminalSessionTools(reg, bg))
	dir := t.TempDir()
	_, err := reg.Run(context.Background(), "terminal_open", map[string]any{"command": "true"}, tools.ToolContext{
		SessionID: "s", ProjectID: "p",
		Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}},
	})
	if err == nil {
		t.Fatal("expected TERMINAL_UNSUPPORTED on windows")
	}
	var rej *tools.ToolReject
	if !errors.As(err, &rej) || rej.Code != "TERMINAL_UNSUPPORTED" {
		t.Fatalf("err = %v, want TERMINAL_UNSUPPORTED", err)
	}
}
