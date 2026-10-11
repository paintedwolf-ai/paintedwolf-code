package native

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/terminal"
)

// Every observation mode states the box, including the one that observes
// nothing, so a refused bind is attributable to the sandbox rather than read
// as the child's own errno.
func TestEveryTerminalResultStatesTheBox(t *testing.T) {
	if testing.Short() {
		t.Skip("pty")
	}
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	bg := newTestBackgroundRegistry(t)
	reg := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register", RegisterTerminalSessionTools(reg, bg))

	dir := t.TempDir()
	tctx := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "sess",
			ProjectID: "proj"},
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
			if !strings.Contains(raw, `"confined"`) {
				t.Fatalf("observe=%s omitted the confinement fields: %s", observe, raw)
			}
		})
	}
}
