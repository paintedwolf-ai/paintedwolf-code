package hostcontracts

import (
	"errors"
	"net"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
)

func TestResolveListenAddrDefault(t *testing.T) {
	t.Setenv("LYCAON_ADDR", "")
	t.Setenv("LYCAON_BIND_ALL", "")

	addr, err := hostapi.ResolveListenAddr()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "127.0.0.1:8787" {
		t.Fatalf("addr = %q, want 127.0.0.1:8787", addr)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host/port: %v", err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("default bind host = %q, want a loopback address", host)
	}
}

func TestResolveListenAddrLoopbackOverride(t *testing.T) {
	t.Setenv("LYCAON_ADDR", "127.0.0.1:9999")
	t.Setenv("LYCAON_BIND_ALL", "")

	addr, err := hostapi.ResolveListenAddr()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "127.0.0.1:9999" {
		t.Fatalf("addr = %q", addr)
	}
}

func TestResolveListenAddrRejectsWildcardWithoutOptIn(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8787", "[::]:8787"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("LYCAON_ADDR", addr)
			t.Setenv("LYCAON_BIND_ALL", "")

			got, err := hostapi.ResolveListenAddr()
			if !errors.Is(err, hostapi.ErrWildcardBindNeedsOptIn) {
				t.Fatalf("err = %v, want ErrWildcardBindNeedsOptIn", err)
			}
			if got != "" {
				t.Fatalf("refused bind returned address %q", got)
			}
		})
	}
}

func TestResolveListenAddrAllowsWildcardWithOptIn(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:8787", "[::]:8787"} {
		t.Run(addr, func(t *testing.T) {
			t.Setenv("LYCAON_ADDR", addr)
			t.Setenv("LYCAON_BIND_ALL", "1")

			got, err := hostapi.ResolveListenAddr()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != addr {
				t.Fatalf("addr = %q, want %q", got, addr)
			}
		})
	}
}

func TestResolveListenAddrRejectsNonLoopbackWithoutOptIn(t *testing.T) {
	t.Setenv("LYCAON_ADDR", "192.168.1.10:8787")
	t.Setenv("LYCAON_BIND_ALL", "")

	got, err := hostapi.ResolveListenAddr()
	if !errors.Is(err, hostapi.ErrNonLoopbackBindNeedsOptIn) {
		t.Fatalf("err = %v, want ErrNonLoopbackBindNeedsOptIn", err)
	}
	if got != "" {
		t.Fatalf("refused bind returned address %q", got)
	}
}
