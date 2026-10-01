package detectionpack

import (
	"strings"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/ptyinput"
)

// Terminal input is decoded through the line discipline before matching.
var keystrokeArgs = map[string]string{
	"terminal_send": "input",
}

// CommandTextForTool projects command text for execution and secret matching.
func CommandTextForTool(tool string, args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	if key, keystrokes := keystrokeArgs[strings.TrimSpace(tool)]; keystrokes {
		typed, _ := args[key].(string)
		return strings.Join(ptyinput.DecodeLines(typed), "\n")
	}
	return commandsurface.PrimaryCommandLine(args, nil)
}
