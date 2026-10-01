package contract

import (
	"net"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
)

// TestServeBindsLoopbackOnly guards the sidecar loopback invariant for CI.
func TestServeBindsLoopbackOnly(t *testing.T) {
	t.Parallel()
	os.Unsetenv("LYCAON_ADDR")
	os.Unsetenv("LYCAON_BIND_ALL")

	addr, err := api.ResolveListenAddr()
	if err != nil {
		t.Fatalf("ResolveListenAddr: %v", err)
	}

	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	if host != "127.0.0.1" && host != "localhost" {
		t.Fatalf("default bind host = %q, want loopback", host)
	}
}

// TestWildcardBindRequiresOptIn ensures 0.0.0.0 cannot be used without LYCAON_BIND_ALL=1.
func TestWildcardBindRequiresOptIn(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8787", "[::]:8787"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("LYCAON_ADDR", addr)
			os.Unsetenv("LYCAON_BIND_ALL")

			if _, err := api.ResolveListenAddr(); err == nil {
				t.Fatalf("expected error for %q without LYCAON_BIND_ALL", addr)
			}
		})
	}
}
