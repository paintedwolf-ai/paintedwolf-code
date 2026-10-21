//go:build unix && !linux

package exec

import "os/exec"

// relayedCommand reports a command that is the broker relay, which only the
// protected Linux launch uses.
func relayedCommand(*exec.Cmd) bool { return false }
