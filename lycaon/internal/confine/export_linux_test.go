//go:build linux

package confine

import "testing"

// SetHostResolversForTest replaces the resolvers a direct IP sandbox relays to.
func SetHostResolversForTest(t *testing.T, resolvers ...string) {
	t.Helper()
	previous := hostResolvers
	hostResolvers = func() []string { return resolvers }
	t.Cleanup(func() { hostResolvers = previous })
}
