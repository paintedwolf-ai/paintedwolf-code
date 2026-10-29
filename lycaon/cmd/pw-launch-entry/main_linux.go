//go:build linux

// pw-launch-entry seals the unprivileged child of the protected bootstrap
// before executing its descriptor-bound desktop image, or, started by the
// privileged stage as the broker agent, serves the launch broker.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/lycaon/lycaon/internal/launchguard"
)

func init() {
	runtime.LockOSThread()
	launchguard.ScrubBootstrapEnvironment()
}

func main() {
	os.Exit(run(os.Args, os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	var err error
	failureStatus := 1
	switch {
	case len(args) == 2 && args[1] == launchguard.GuardianArgument:
		status, guardianErr := launchguard.RunGuardian()
		if guardianErr != nil {
			return 1
		}
		return status
	case len(args) == 1:
		err = launchguard.EnterDesktop()
	case len(args) == 2 && args[1] == launchguard.MountIntakeArgument:
		err = launchguard.RunMountIntake()
		failureStatus = launchguard.MountIntakeExitStatus(err)
	case len(args) == 2 && args[1] == launchguard.MountViewArgument:
		err = launchguard.RunMountView()
	case len(args) == 2 && args[1] == launchguard.MountSourceArgument:
		err = launchguard.RunMountSource()
	case len(args) == 2 && args[1] == launchguard.BrokerAgentArgument:
		err = launchguard.RunBrokerAgent()
	default:
		err = fmt.Errorf("invalid protected entry invocation")
	}
	if err != nil {
		if launchguard.DropPrivilegesForError() {
			_, _ = fmt.Fprintf(stderr, "Painted Wolf Code: protected launch refused: %v\n", err)
		}
		return failureStatus
	}
	return 0
}
