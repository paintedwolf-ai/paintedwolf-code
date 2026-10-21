//go:build linux

// pw-launch-entry seals the unprivileged child of the protected bootstrap
// before executing its descriptor-bound desktop image, or, started by the
// privileged stage as the broker agent, serves the launch broker.
package main

import (
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/launchguard"
)

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = launchguard.EnterDesktop()
	case len(os.Args) == 2 && os.Args[1] == launchguard.BrokerAgentArgument:
		err = launchguard.RunBrokerAgent()
	default:
		err = fmt.Errorf("invalid protected entry invocation")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Painted Wolf Code: protected launch refused: %v\n", err)
		os.Exit(1)
	}
}
