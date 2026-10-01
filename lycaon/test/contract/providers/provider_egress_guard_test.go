package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestProviderHTTPUsesEgressGuard checks guarded provider fan-out.
func TestProviderHTTPUsesEgressGuard(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	restPath := filepath.Join(root, "lycaon", "internal", "webresearch", "rest_provider.go")
	httpPath := filepath.Join(root, "lycaon", "internal", "webresearch", "provider_http.go")
	egressPath := filepath.Join(root, "lycaon", "internal", "webresearch", "provider_egress.go")
	restBody, err := os.ReadFile(restPath)
	contractcheck.FailErr(t, "read file", err)
	httpBody, err := os.ReadFile(httpPath)
	contractcheck.FailErr(t, "read provider HTTP", err)
	egressBody, err := os.ReadFile(egressPath)
	contractcheck.FailErr(t, "read file", err)
	rest := string(restBody)
	egress := string(egressBody)

	for path, body := range map[string]string{restPath: rest, httpPath: string(httpBody)} {
		for _, forbidden := range []string{"http.DefaultClient", "ProxyFromEnvironment"} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s must not use %s for provider dials", path, forbidden)
			}
		}
	}
	if !strings.Contains(rest, "doProviderHTTP") {
		t.Fatalf("%s must route provider requests through doProviderHTTP", restPath)
	}
	if !strings.Contains(string(httpBody), "providerEgressClient") {
		t.Fatalf("%s must call providerEgressClient before dialing", httpPath)
	}
	if !strings.Contains(egress, "validateProviderEndpoint") {
		t.Fatalf("%s must define validateProviderEndpoint", egressPath)
	}
	if !strings.Contains(egress, "resolveProviderEgressIPs") {
		t.Fatalf("%s must resolve via resolveProviderEgressIPs", egressPath)
	}
	if !strings.Contains(egress, "egress.PinnedTransport") {
		t.Fatalf("%s must pin dials via egress.PinnedTransport", egressPath)
	}
}
