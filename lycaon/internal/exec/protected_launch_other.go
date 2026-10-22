//go:build !linux

package exec

import "os/exec"

func relayThroughBroker(*exec.Cmd) error { return nil }

// relayedCommand reports a command that is the broker relay, which only the
// protected Linux launch uses.
func relayedCommand(*exec.Cmd) bool { return false }
