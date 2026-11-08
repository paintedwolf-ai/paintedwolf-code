package toolfeedback

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestCommandRecoveryFollowsInvocationMode(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, tc := range []struct {
		name, tool, source string
		capture            bool
		cause              error
		steeredTool        string
		steered, blocked   string
	}{
		{"capture sequence", "command", "tool; echo $?", true, commandsurface.ErrSequenceUnsupported,
			"command", "Run the program text through the command tool", "The command tool is not offered"},
		{"capture pipe", "command", "tool | tee output", true, argv.ErrShellMetacharacters,
			"command", "Run the program text through the command tool", "The command tool is not offered"},
		{"terminal pipe", "terminal_open", "tool | tee output", false, argv.ErrShellMetacharacters,
			"command", "Run the program text through the command tool", "The command tool is not offered"},
		{"terminal environment", "terminal_open", "VALUE=one tool", false, argv.ErrEnvAssignmentCommand,
			"terminal_open", "leading assignments become that program's environment", "Name the program the assignments apply to"},
		{"terminal directory", "terminal_open", "cd src && tool", false, commandsurface.ErrDirectoryChangeCommand,
			"terminal_open", "in its `cwd` argument", "Name the program's working directory"},
	} {
		// The reader's own tool is always on their request; the command tool is
		// the extra offer the reissue can steer to.
		reachVariants := [][]string{
			nil,
			{tc.tool},
			{tc.tool, "command"},
		}
		for _, offered := range reachVariants {
			steered := slices.Contains(offered, tc.steeredTool)
			name := tc.name
			if len(offered) == 0 {
				name += "/nothing offered"
			} else if steered {
				name += "/steered"
			} else {
				name += "/own tool only"
			}
			t.Run(name, func(t *testing.T) {
				args := map[string]any{"command": tc.source}
				if tc.capture {
					args["terminal_capture"] = map[string]any{}
				}
				rejected := toolrejection.CommandSurfaceObservation(tc.tool, "implement", tc.source, args, tc.cause)
				if rejected == nil {
					t.Fatal("missing typed argv refusal")
				}
				ctx := tools.WithCallReach(t.Context(), toolreach.Offered(offered))
				err := bp.RejectObservation(ctx, tc.tool, "implement", args, rejected)
				refusal, ok := guidance.RefusalFromError(err)
				if !ok {
					t.Fatalf("missing argv feedback: %v", err)
				}
				fix := strings.TrimSpace(refusal.Copy["fix"])
				want := tc.steered
				if !steered {
					want = tc.blocked
				}
				if !strings.Contains(fix, want) {
					t.Fatalf("recovery copy %q misses %q", fix, want)
				}
				if tc.capture && (strings.Contains(fix, "pipeline") || strings.Contains(fix, "env") || strings.Contains(fix, "background")) {
					t.Fatalf("captured argv acquired program arguments: %s", fix)
				}
				if tc.capture && errors.Is(tc.cause, commandsurface.ErrSequenceUnsupported) {
					cause := strings.TrimSpace(refusal.Copy["cause"])
					if !strings.Contains(cause, "terminal_capture runs one program") || !strings.Contains(cause, "`|`") {
						t.Fatalf("capture sequence cause %q omits terminal_capture pipe guidance", cause)
					}
				}
			})
		}
	}
}
