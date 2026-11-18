package webresearch_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/admissionfixture"
)

func assertBrowserLoopbackConfinement(t *testing.T) {
	t.Helper()
	admissionfixture.Enable(t)
	t.Setenv("LYCAON_BYPASS_PERMISSIONS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	c, ok := confine.BrowserConfinement(confine.Request{Roots: []string{"/proj"}})
	if !ok || c == nil {
		t.Fatal("BrowserConfinement should be available on this host")
	}
	if c.Network != confine.NetworkDeny {
		t.Fatalf("BrowserConfinement.Network=%v, want NetworkDeny (not command proxy-only)", c.Network)
	}
	if !c.LoopbackConnect {
		t.Fatal("BrowserConfinement must keep host-local connect on the floor")
	}
}
