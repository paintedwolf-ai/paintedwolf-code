package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/argv"
	"github.com/lycaon/lycaon/internal/commandsurface"
)

func TestCommandRecoveryFollowsCaptureMode(t *testing.T) {
	bp := stockBlockPlane(t)
	for _, tc := range []struct {
		name, tool, command string
		capture             bool
		parseErr            error
		want                string
	}{
		{"capture sequence", "command", "tool --check; echo $?", true, commandsurface.ErrSequenceUnsupported, "omit `terminal_capture`"},
		{"capture pipe", "command", "tool --check | tee output.txt", true, argv.ErrShellMetacharacters, "omit `terminal_capture`"},
		{"ordinary pipe", "command", "tool --check | tee output.txt", false, argv.ErrShellMetacharacters, "Use the `pipeline` array"},
		{"terminal pipe", "terminal_open", "tool --check | tee output.txt", false, argv.ErrShellMetacharacters, "run the composed form under `command`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"command": tc.command}
			fields := []string{"command", "cwd", "env"}
			if tc.tool == "command" {
				fields = append(fields, "pipeline", "stdin", "stdout_to", "stderr_to", "background", "terminal_capture")
			}
			if tc.capture {
				args["terminal_capture"] = map[string]any{"caption": "Final check"}
			}
			reject := CommandSurfaceObservation(tc.tool, "implement", tc.command, args, fields, tc.parseErr)
			if reject == nil || reject.Data["terminal_capture"] != tc.capture {
				t.Fatalf("capture observation = %#v, want capture=%v", reject, tc.capture)
			}
			err := bp.RejectObservation(context.Background(), tc.tool, "implement", args, reject)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("recovery = %v, want %q", err, tc.want)
			}
			if tc.capture {
				for _, forbidden := range []string{"run the composed form under `command`", "Use the `pipeline` array instead"} {
					if strings.Contains(err.Error(), forbidden) {
						t.Fatalf("capture recovery offers unsupported retry %q: %v", forbidden, err)
					}
				}
				if !strings.Contains(err.Error(), "read `exit_code` from its result") {
					t.Fatalf("capture recovery omits the existing exit receipt: %v", err)
				}
			}
		})
	}
}
