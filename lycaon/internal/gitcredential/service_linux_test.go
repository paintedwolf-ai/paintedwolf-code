//go:build linux

package gitcredential

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The test process stands in for the served git process and for the helper.
func serveForSelf(t *testing.T, store backend) *Service {
	t.Helper()
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)
	service, err := Serve(t.Context(), ServeOptions{GitBinary: self, Store: store})
	testutil.FailErr(t, "serve", err)
	t.Cleanup(func() { _ = service.Close() })
	return service
}

func TestServiceAnswersHelperOperationsForVerifiedPeers(t *testing.T) {
	store := developmentVault(t)
	service := serveForSelf(t, store)
	var out bytes.Buffer
	testutil.FailErr(t, "store", Forward("store", service.Path(), strings.NewReader("protocol=https\nhost=example.com\nusername=alice\npassword=secret\n\n"), &out))
	testutil.FailErr(t, "get", Forward("get", service.Path(), strings.NewReader("protocol=https\nhost=example.com\n\n"), &out))
	if out.String() != "username=alice\npassword=secret\n\n" {
		t.Fatalf("get output = %q", out.String())
	}
	out.Reset()
	testutil.FailErr(t, "erase", Forward("erase", service.Path(), strings.NewReader("protocol=https\nhost=example.com\n"), &out))
	testutil.FailErr(t, "get after erase", Forward("get", service.Path(), strings.NewReader("protocol=https\nhost=example.com\n\n"), &out))
	if out.Len() != 0 {
		t.Fatalf("erased credential served: %q", out.String())
	}
	if err := Forward("steal", service.Path(), strings.NewReader(""), &out); err == nil {
		t.Fatal("unknown operation was forwarded")
	}
	if err := Forward("get", "", strings.NewReader(""), &out); err == nil {
		t.Fatal("missing socket was accepted")
	}
	path := service.Path()
	testutil.FailErr(t, "close", service.Close())
	testutil.FailErr(t, "repeat close", service.Close())
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("socket remained after close: %v", err)
	}
}

func TestServiceRefusesPeersOutsideTheServedGitProcess(t *testing.T) {
	store := developmentVault(t)
	testutil.FailErr(t, "seed", store.Store(Credential{Protocol: "https", Host: "example.com", Username: "alice", Password: "secret"}))
	service, err := Serve(t.Context(), ServeOptions{GitBinary: "/bin/sh", Store: store})
	testutil.FailErr(t, "serve for a foreign git", err)
	defer func() { _ = service.Close() }()
	var out bytes.Buffer
	_ = Forward("get", service.Path(), strings.NewReader("protocol=https\nhost=example.com\n\n"), &out)
	if out.Len() != 0 {
		t.Fatalf("foreign peer received a credential: %q", out.String())
	}
	if _, err := Serve(t.Context(), ServeOptions{GitBinary: ""}); err == nil {
		t.Fatal("service started without a served git binary")
	}
	if _, err := Serve(t.Context(), ServeOptions{GitBinary: "/missing/git"}); err == nil {
		t.Fatal("service started for a missing git binary")
	}
}

func TestServiceCancellationClosesTheSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)
	service, err := Serve(ctx, ServeOptions{GitBinary: self, Store: developmentVault(t)})
	testutil.FailErr(t, "serve", err)
	cancel()
	deadline := make(chan struct{})
	go func() { service.wg.Wait(); close(deadline) }()
	<-deadline
	if conn, err := net.Dial("unix", service.Path()); err == nil {
		_ = conn.Close()
		t.Fatal("socket accepted connections after cancellation")
	}
}

func TestServicePeerVerificationReadsKernelFacts(t *testing.T) {
	parent, err := parentProcess(os.Getpid())
	testutil.FailErr(t, "parent process", err)
	if parent != os.Getppid() {
		t.Fatalf("parent process = %d, want %d", parent, os.Getppid())
	}
	exe, err := processExecutable(os.Getpid())
	testutil.FailErr(t, "process executable", err)
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)
	if !sameFile(exe, self) {
		t.Fatalf("executable = %q, want %q", exe, self)
	}
	if _, err := parentProcess(1 << 22); err == nil {
		t.Fatal("absent process had a parent")
	}
	// A child shell connecting through this executable's helper verb is the production shape.
	store := developmentVault(t)
	testutil.FailErr(t, "seed", store.Store(Credential{Protocol: "https", Host: "child.example", Username: "alice", Password: "secret"}))
	service := serveForSelf(t, store)
	cmd := exec.Command(self, "-test.run", "TestHelperChildProcess")
	cmd.Env = append(os.Environ(), "PW_HELPER_CHILD=1", SocketEnv+"="+service.Path())
	cmd.Stdin = strings.NewReader("protocol=https\nhost=child.example\n\n")
	output, err := cmd.Output()
	testutil.FailErr(t, "run helper child", err)
	if !strings.Contains(string(output), "password=secret") {
		t.Fatalf("child helper output = %q", output)
	}
}

func TestHelperChildProcess(t *testing.T) {
	if os.Getenv("PW_HELPER_CHILD") != "1" {
		t.Skip("helper child entry point")
	}
	if err := Forward("get", os.Getenv(SocketEnv), os.Stdin, os.Stdout); err != nil {
		t.Fatalf("forward: %v", err)
	}
}
