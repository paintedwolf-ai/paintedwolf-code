package toolcommand

import "strings"

// gitAmendReplacement represents an amend with a message and explicit paths.
func gitAmendReplacement(args []string) (ReplacementCall, bool) {
	var message string
	amend := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--amend":
			if amend {
				return ReplacementCall{}, false
			}
			amend = true
		case "--only", "-o":
		case "-m", "--message":
			i++
			if i >= len(args) || message != "" {
				return ReplacementCall{}, false
			}
			message = args[i]
		case "--":
			paths := args[i+1:]
			if !amend || strings.TrimSpace(message) == "" || len(paths) == 0 {
				return ReplacementCall{}, false
			}
			return ReplacementCall{Tool: "git_commit", Args: map[string]any{
				"message": message, "paths": stringsToAny(paths), "amend": true,
			}}, true
		default:
			return ReplacementCall{}, false
		}
	}
	return ReplacementCall{}, false
}
