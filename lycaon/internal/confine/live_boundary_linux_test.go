//go:build linux

package confine_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

// Linux reports the socket calls its supervisor refuses and the writes a floor
// refuses. Landlock has no stream of its own refusals an unprivileged process
// can read, so the record is never complete.
const liveRefusalWitness = confine.WitnessIncomplete

// The PID namespace bounds a confined process's signals with or without
// process control; the native process_signal tool reaches outside it.
const processControlSignalsOutside = false

// registerResolvableName makes a name only the host's resolver answers: the
// plan's resolver is a local server, since /etc/resolv.conf belongs to the host.
func registerResolvableName(t *testing.T) string {
	t.Helper()
	confine.SetHostResolversForTest(t, fakeResolver(t))
	return "pw-confine-probe.example"
}

// requireLiveBoundary returns the helper that applies the host's real boundary,
// failing where Linux confinement should exist and does not.
func requireLiveBoundary(t *testing.T) string {
	t.Helper()
	return helperBinary(t, requireLinuxSandbox(t))
}

// liveBoundaryRoots adds what a confined helper needs beside roots.
func liveBoundaryRoots(roots ...string) []string { return sandboxRoots(roots...) }
