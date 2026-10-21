//go:build linux

// pw-launch is the installed setuid bootstrap of the protected Linux desktop.
// Its privileged half only selects the administrator's generation, binds it,
// and creates the protected namespace; it then remains as the caller's
// unprivileged supervisor. See docs/security.md, Protected Linux launch.
package main

import (
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/launchguard"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == launchguard.StageArgument {
		if err := launchguard.RunStage(); err != nil {
			fmt.Fprintf(os.Stderr, "Painted Wolf Code: protected launch refused: %v\n", err)
			os.Exit(1)
		}
		return
	}
	status, err := launchguard.RunBootstrap(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Painted Wolf Code: protected launch refused: %v\n", err)
	}
	os.Exit(status)
}
