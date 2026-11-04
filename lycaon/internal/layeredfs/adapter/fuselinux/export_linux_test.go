//go:build linux

package fuselinux

// ThreadGroupDyingForTest exposes the dead-server proof over a fabricated proc directory.
func ThreadGroupDyingForTest(procDir string) bool { return threadGroupDying(procDir) }
