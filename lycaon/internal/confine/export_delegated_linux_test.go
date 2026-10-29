//go:build linux

package confine

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
)

// UseDelegatedEndpointsForTest stands a fixture socket in for a delegated
// service endpoint, which tests cannot create at its operating-system path.
func UseDelegatedEndpointsForTest(t *testing.T, endpoints []linuxsandbox.DelegatedEndpoint) {
	t.Helper()
	previous := delegatedEndpoints
	delegatedEndpoints = endpoints
	t.Cleanup(func() { delegatedEndpoints = previous })
}
