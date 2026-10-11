package native

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/terminal"
)

// TestTerminalOpenArgvParity checks single-argv parsing without shell syntax.
func TestTerminalOpenArgvParity(t *testing.T) {
	bg := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	dir := t.TempDir()
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "s",
			ProjectID: "p",
			Agent:     "implement"},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}}},
	}
	_, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
		"command": "curl https://example.com; rm -rf /",
	}, tctx)
	if err == nil {
		t.Fatal("expected shell-metacharacter deny")
	}
	if !commandsurface.IsCommandSurfaceError(err) {
		t.Fatalf("err = %v, want command-surface deny", err)
	}
}
