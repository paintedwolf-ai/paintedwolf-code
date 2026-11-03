package toolfeedback

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestCommandRecoveryFollowsReceivingSchemaAndCaptureMode(t *testing.T) {
	bp := stockBlockPlane(t)
	schemas, err := toolschema.LoadSchemaDir(filepath.Join(testutil.CheckoutRoot(t), "lycaon/config/packs/painted-wolf/platform/tools/schemas"))
	testutil.FailErr(t, "load receiving schemas", err)
	for _, tc := range []struct {
		name, tool, source, field string
		capture                   bool
		cause                     error
	}{
		{"capture sequence", "command", "tool; echo $?", "exit_code", true, commandsurface.ErrSequenceUnsupported},
		{"capture pipe", "command", "tool | tee output", "exit_code", true, argv.ErrShellMetacharacters},
		{"verify stage", "verify", "tool | head", "pipeline", false, argv.ErrShellMetacharacters},
		{"terminal pipe", "terminal_open", "tool | tee output", "", false, argv.ErrShellMetacharacters},
		{"terminal environment", "terminal_open", "VALUE=one tool", "env", false, argv.ErrEnvAssignmentCommand},
		{"terminal directory", "terminal_open", "cd src && tool", "cwd", false, commandsurface.ErrDirectoryChangeCommand},
	} {
		for _, commandOffered := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				args := map[string]any{"command": tc.source}
				if tc.capture {
					args["terminal_capture"] = map[string]any{}
				}
				meta, ok := schemas.ToolMeta(tc.tool)
				if !ok {
					t.Fatalf("missing %s schema", tc.tool)
				}
				rejected := toolrejection.CommandSurfaceObservation(tc.tool, "implement", tc.source, args, toolschema.ArgFieldPaths(meta.ArgsSchema), tc.cause)
				if rejected == nil {
					t.Fatal("missing typed argv refusal")
				}
				var offered []string
				if commandOffered {
					offered = []string{"command"}
				}
				ctx := tools.WithRecoveryTools(t.Context(), offered)
				err := bp.RejectObservation(ctx, tc.tool, "implement", args, rejected)
				refusal, ok := guidance.RefusalFromError(err)
				if !ok {
					t.Fatalf("missing argv feedback: %v", err)
				}
				fix := strings.TrimSpace(refusal.Copy["fix"])
				if (tc.field == "" && fix != "") || (tc.field != "" && !strings.Contains(fix, "`"+tc.field+"`")) {
					t.Fatalf("schema or mode acquired the wrong repair: %s", fix)
				}
				if (strings.TrimSpace(refusal.Copy["instead"]) != "") != commandOffered {
					t.Fatalf("unoffered program tool became a recovery: %#v", refusal.Copy)
				}
				if tc.capture && (strings.Contains(fix, "pipeline") || strings.Contains(fix, "env") || strings.Contains(fix, "background")) {
					t.Fatalf("captured argv acquired program arguments: %s", fix)
				}
			})
		}
	}
}
