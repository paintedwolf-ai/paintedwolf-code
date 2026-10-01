package webresearch_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/webresearch"
)

// Structured web tools use the host HTTP stack, not command DefaultConfinement.
func TestWebToolsUnaffectedByCommandProxyOnlyDefault(t *testing.T) {
	if webresearch.SearchToolName != "web_search" || webresearch.FetchURLToolName != "fetch_url" {
		t.Fatalf("web tool ids drifted: search=%q fetch=%q",
			webresearch.SearchToolName, webresearch.FetchURLToolName)
	}

	cfg := webresearch.NewConfigStoreAt(t.TempDir() + "/cfg.yaml")
	deny := webresearch.SearchToolRuntimeDeny(cfg)
	if deny("web_search") || deny("fetch_url") {
		t.Fatal("direct-search happy path must stay open across command proxy-only flip")
	}

	if confine.Available() {
		confine.TestingSetAutoConfine(t)
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

	if err := webresearch.ValidateProviderConfigEndpoint(
		context.Background(), "https://search.brave.com/", false,
	); err != nil {
		t.Fatalf("provider endpoint validation must stay open for public HTTPS: %v", err)
	}
}
