package main

import (
	"fmt"
	"io"

	"github.com/lycaon/lycaon/internal/version"
)

// isCLIVerb reports a reserved first-token subcommand. Anything else (including
// a bare path, --new, or project name) is treated as open argv.
func isCLIVerb(name string) bool {
	switch name {
	case "serve", "open", "ls", "logs", "completion", "diagnostics", "credentials",
		"scan", "workflow", "extensions", "rules", "prompts", "browser", "decide", "git-credential-osxkeychain",
		"git-credential-vault", "internal-scan-worker", "internal-system-proxy":
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
	dispatchVersion
)

// versionFlag is a flag rather than a verb so a project named "version" still opens.
const versionFlag = "--version"

func classifyArgs(args []string) (dispatchKind, string, []string) {
	if len(args) == 0 {
		return dispatchOpen, "open", nil
	}
	if args[0] == versionFlag {
		return dispatchVersion, versionFlag, args[1:]
	}
	if isCLIVerb(args[0]) {
		return dispatchVerb, args[0], args[1:]
	}
	return dispatchOpen, "open", args
}

// printVersion reports the build's product version; release builds stamp it.
func printVersion(w io.Writer, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%s takes no arguments", versionFlag)
	}
	_, err := fmt.Fprintf(w, "Painted Wolf Code %s\n", version.Version)
	return err
}
