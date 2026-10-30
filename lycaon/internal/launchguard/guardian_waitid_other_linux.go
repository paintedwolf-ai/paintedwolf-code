//go:build linux && !amd64 && !arm64

package launchguard

// No child-union layout is assumed on other Linux architectures.
func observeGuardianExit(int, int, uint32) *guardianExitFact { return nil }
