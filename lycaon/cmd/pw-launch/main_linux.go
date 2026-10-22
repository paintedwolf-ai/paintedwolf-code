//go:build linux

// pw-launch is the installed setuid bootstrap of the protected Linux desktop.
// Its privileged half only selects the administrator's generation, binds it,
// and creates the protected namespace; it then remains as the caller's
// unprivileged supervisor. See docs/security.md, Protected Linux launch.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/launchguard"
)

func init() {
	launchguard.ScrubBootstrapEnvironment()
}

func main() {
	os.Exit(run(os.Args, os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) == 2 && args[1] == launchguard.StageArgument {
		if err := launchguard.RunStage(); err != nil {
			if launchguard.DropPrivilegesForError() {
				_, _ = fmt.Fprintf(stderr, "Painted Wolf Code: protected launch refused: %v\n", err)
			}
			return 1
		}
		return 0
	}
	status, err := launchguard.RunBootstrap(args[1:])
	if err != nil {
		if launchguard.DropPrivilegesForError() {
			_, _ = fmt.Fprintf(stderr, "Painted Wolf Code: protected launch refused: %v\n", err)
		}
	}
	return status
}
