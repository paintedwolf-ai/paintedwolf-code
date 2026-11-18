// Package admissionfixture activates the host's confinement policy for a test.
package admissionfixture

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine/internal/admissionstate"
)

func Enable(t testing.TB) {
	t.Helper()
	previous := admissionstate.ProcessEnabled.Swap(true)
	t.Cleanup(func() { admissionstate.ProcessEnabled.Store(previous) })
}
