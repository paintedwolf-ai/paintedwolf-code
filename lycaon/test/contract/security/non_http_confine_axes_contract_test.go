package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

// No LYCAON_SANDBOX_NETWORK spelling opens ambient network: the open-sounding
// aliases keep the mediated default, while "deny" shows the variable is read.
func TestContractNoAmbientNetworkAllowAlias(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	requireNonHTTPConfinement(t)
	root := t.TempDir()
	network := func(value string) confine.NetworkMode {
		t.Helper()
		t.Setenv("LYCAON_SANDBOX_NETWORK", value)
		c, ok := confine.DefaultConfinement(confine.Request{Roots: []string{root}})
		if !ok || c == nil {
			t.Fatalf("LYCAON_SANDBOX_NETWORK=%q: no confinement", value)
		}
		return c.Network
	}
	if got := network("deny"); got != confine.NetworkDeny {
		t.Fatalf("LYCAON_SANDBOX_NETWORK=deny gave network %v, want deny", got)
	}
	for _, alias := range []string{"", "allow", "unrestricted", "off", "open", "ALLOW"} {
		if got := network(alias); got != confine.NetworkProxyOnly {
			t.Errorf("LYCAON_SANDBOX_NETWORK=%q gave network %v, want the mediated default", alias, got)
		}
	}
}
