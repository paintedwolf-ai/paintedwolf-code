package main

// isCLIVerb reports a reserved first-token subcommand. Anything else (including
// a bare path, --new, or project name) is treated as open argv.
func isCLIVerb(name string) bool {
	switch name {
	case "serve", "open", "ls", "logs", "completion", "diagnostics", "credentials",
		"scan", "workflow", "extensions", "rules", "prompts", "browser", "decide", "git-credential-osxkeychain",
		"internal-scan-worker":
		return true
	default:
		return false
	}
}

// dispatchKind classifies argv for the top-level router (testable without I/O).
type dispatchKind int

const (
	dispatchOpen dispatchKind = iota
	dispatchVerb
)

func classifyArgs(args []string) (dispatchKind, string, []string) {
	if len(args) == 0 {
		return dispatchOpen, "open", nil
	}
	if isCLIVerb(args[0]) {
		return dispatchVerb, args[0], args[1:]
	}
	return dispatchOpen, "open", args
}
