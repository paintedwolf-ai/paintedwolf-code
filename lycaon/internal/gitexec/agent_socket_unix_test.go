//go:build darwin || linux

package gitexec

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func agentFixtureDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp(os.TempDir(), "pwa-") //nolint:usetesting // Unix socket paths must fit sockaddr_un.
	if err != nil {
		t.Fatalf("create socket fixture directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func agentFixtureSocket(t *testing.T) string {
	t.Helper()
	path := filepath.Join(agentFixtureDirectory(t), "agent")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on agent fixture: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return path
}

func TestAgentSocketValidationRequiresAnExistingUnixSocket(t *testing.T) {
	socket := agentFixtureSocket(t)
	if got := validAgentSocket(socket); got != socket {
		t.Fatalf("valid socket = %q, want %q", got, socket)
	}
	file := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatalf("write regular fixture: %v", err)
	}
	for _, path := range []string{"", "relative", file, t.TempDir(), socket + "\nother", socket + "\x00", socket + "\r"} {
		if got := validAgentSocket(path); got != "" {
			t.Fatalf("accepted invalid socket path %q", path)
		}
	}
}

func TestAgentSocketValidationPreservesNativePathIdentity(t *testing.T) {
	directory := agentFixtureDirectory(t)
	real := filepath.Join(directory, "real")
	if err := os.MkdirAll(filepath.Join(real, "nested"), 0700); err != nil {
		t.Fatalf("create native socket directories: %v", err)
	}
	path := filepath.Join(real, "agent λ ")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on native socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(filepath.Join(real, "nested"), alias); err != nil {
		t.Fatalf("create native directory alias: %v", err)
	}
	for _, path := range []string{path, alias + "/../agent λ "} {
		if got := validAgentSocket(path); got != path {
			t.Fatalf("native socket path = %q, want %q", got, path)
		}
	}
}
