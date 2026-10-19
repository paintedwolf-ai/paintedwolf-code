package toolexecution

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestTerminalScratchOperandExpansion(t *testing.T) {
	root, scratch := t.TempDir(), t.TempDir()
	for _, tool := range []string{"command", "verify", "terminal_open"} {
		tc := tools.ToolContext{Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root"}, Host: tools.InvocationHost{SessionScratchDir: scratch}}
		args := map[string]any{"command": "node @scratch/test-app.mjs", "observe": "screen"}
		got, err := (&Boundary{}).expandCommandOperands(t.Context(), tool, "coordinator", args, &tc)
		testutil.FailErr(t, "expand scratch operand", err)
		plan, err := commandsurface.ParsePlan(got)
		testutil.FailErr(t, "parse expanded command", err)
		want := filepath.Join(scratch, "test-app.mjs")
		if len(plan.Stages) != 1 || len(plan.Stages[0].Args) != 1 || plan.Stages[0].Args[0] != want {
			t.Fatalf("%s expanded stages = %+v, want %q", tool, plan.Stages, want)
		}
		if tc.Effects.RequestedArgs["command"] != args["command"] || got["observe"] != "screen" {
			t.Fatalf("%s lost request display or terminal options", tool)
		}
	}
}
