//go:build !windows

package native

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/terminal"
)

// Every observation mode states the box, including the one that observes
// nothing, so a refused bind is attributable to the sandbox rather than read
// as the child's own errno.
func TestEveryTerminalResultStatesTheBox(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "0")
	t.Setenv("LYCAON_SANDBOX", "on")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
	bg := newTestBackgroundRegistry(t)
	t.Cleanup(func() { _ = bg.Lifecycle.Close(context.Background()) })
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	dir := t.TempDir()
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess",
			ProjectID: "proj"},
		Host:   tools.InvocationHost{SessionScratchDir: t.TempDir(), HostDataDir: t.TempDir()},
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "main", Path: dir, IsPrimary: true}}},
	}
	for _, observe := range []string{"ack", "delta", "screen"} {
		t.Run(observe, func(t *testing.T) {
			raw, err := reg.Run(context.Background(), terminal.OpenToolName, map[string]any{
				"command": "/bin/echo hello", "observe": observe,
			}, tctx)
			testutil.FailErr(t, "terminal_open", err)
			var opened terminal.OpenResult
			testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(raw), &opened))
			defer func() {
				_, _ = reg.Run(context.Background(), terminal.CloseToolName, map[string]any{"id": opened.ID}, tctx)
			}()
			if !opened.Confined || string(opened.Observe) != observe {
				t.Fatalf("observe=%s boundary=%+v", observe, opened)
			}
		})
	}
}
