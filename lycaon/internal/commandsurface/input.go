package commandsurface

import "strings"

// HasCommandInput reports presence without treating invalid command syntax as absence.
func HasCommandInput(args map[string]any) bool {
	if command, _ := args["command"].(string); strings.TrimSpace(command) != "" {
		return true
	}
	switch pipeline := args["pipeline"].(type) {
	case []any:
		return len(pipeline) > 0
	case []string:
		return len(pipeline) > 0
	default:
		return false
	}
}
