package api

import (
	"os"
	"testing"
)

func TestResolveListenAddrDefault(t *testing.T) {
	os.Unsetenv("LYCAON_ADDR")
	os.Unsetenv("LYCAON_BIND_ALL")

	addr, err := ResolveListenAddr()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != DefaultListenAddr {
		t.Fatalf("addr = %q, want %q", addr, DefaultListenAddr)
	}
}

func TestResolveListenAddrLoopbackOverride(t *testing.T) {
	t.Setenv("LYCAON_ADDR", "127.0.0.1:9999")
	os.Unsetenv("LYCAON_BIND_ALL")

	addr, err := ResolveListenAddr()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "127.0.0.1:9999" {
		t.Fatalf("addr = %q", addr)
	}
}

func TestResolveListenAddrRejectsWildcardWithoutOptIn(t *testing.T) {
	t.Setenv("LYCAON_ADDR", "0.0.0.0:8787")
	os.Unsetenv("LYCAON_BIND_ALL")

	_, err := ResolveListenAddr()
	if err == nil {
		t.Fatal("expected error for wildcard bind without opt-in")
	}
}

func TestResolveListenAddrAllowsWildcardWithOptIn(t *testing.T) {
	t.Setenv("LYCAON_ADDR", "0.0.0.0:8787")
	t.Setenv("LYCAON_BIND_ALL", "1")

	addr, err := ResolveListenAddr()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if addr != "0.0.0.0:8787" {
		t.Fatalf("addr = %q", addr)
	}
}

func TestResolveListenAddrRejectsNonLoopbackWithoutOptIn(t *testing.T) {
	t.Setenv("LYCAON_ADDR", "192.168.1.10:8787")
	os.Unsetenv("LYCAON_BIND_ALL")

	_, err := ResolveListenAddr()
	if err == nil {
		t.Fatal("expected error for non-loopback bind without opt-in")
	}
}
