//go:build linux && !amd64 && !arm64

package launchguard

import "golang.org/x/sys/unix"

// No child-union layout is assumed on other Linux architectures.
func observeGuardianExit(int, int, uint32) *guardianExitFact { return nil }

func observeHandoffExit(int, int) (unix.WaitStatus, bool) { return 0, false }
