package sshproxy_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/sshproxy"
)

func TestProxyCommandRequiresEnv(t *testing.T) {
	t.Setenv("LYCAON_SOCKS_PROXY", "")
	t.Setenv("LYCAON_PROXY_TOKEN", "")
	if err := sshproxy.RunProxyCommand(context.Background(), "example.com", "22"); err == nil {
		t.Fatal("expected env error")
	}
}

func TestProxyCommandRequiresHostPort(t *testing.T) {
	t.Setenv("LYCAON_SOCKS_PROXY", "127.0.0.1:1")
	t.Setenv("LYCAON_PROXY_TOKEN", "tok")
	if err := sshproxy.RunProxyCommand(context.Background(), "", "22"); err == nil {
		t.Fatal("expected host error")
	}
}

func TestProxyCommandRejectsInvalidDestination(t *testing.T) {
	t.Setenv("LYCAON_SOCKS_PROXY", "127.0.0.1:1")
	t.Setenv("LYCAON_PROXY_TOKEN", "tok")
	for _, tc := range []struct{ host, port string }{
		{"bad\nhost", "22"},
		{"example.com", "0"},
		{"example.com", "65536"},
		{"example.com", "-1"},
	} {
		if err := sshproxy.RunProxyCommand(context.Background(), tc.host, tc.port); err == nil {
			t.Fatalf("destination %q:%q was accepted", tc.host, tc.port)
		}
	}
}
