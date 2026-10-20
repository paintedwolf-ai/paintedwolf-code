//go:build linux

// pw-launch-entry seals the unprivileged child of the protected bootstrap
// before executing its descriptor-bound desktop image.
package main

import (
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/launchguard"
)

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Painted Wolf Code: invalid protected entry invocation")
		os.Exit(1)
	}
	if err := launchguard.EnterDesktop(); err != nil {
		fmt.Fprintf(os.Stderr, "Painted Wolf Code: protected launch refused: %v\n", err)
		os.Exit(1)
	}
}
