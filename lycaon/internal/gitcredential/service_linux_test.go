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
	"time"

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
	testutil.FailErr(t, "bind the served invocation", service.Bind(os.Getpid()))
	return service
}

// helperChild runs this test binary's helper verb as a child of the test, the
// production shape of a helper Git started.
func helperChild(t *testing.T, socket, host string) string {
	t.Helper()
	output, _ := helperOperation(t, socket, "get", "protocol=https\nhost="+host+"\n\n")
	return output
}

func helperOperation(t *testing.T, socket, operation, input string) (string, error) {
	t.Helper()
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)
	cmd := exec.Command(self, "-test.run", "^TestHelperChildProcess$")
	cmd.Env = append(os.Environ(), "PW_HELPER_CHILD=1", "PW_HELPER_OPERATION="+operation, SocketEnv+"="+socket)
	cmd.Stdin = strings.NewReader(input)
	output, err := cmd.Output()
	return string(output), err
}

func TestServiceAnswersOnlyTheBoundInvocation(t *testing.T) {
	store := developmentVault(t)
	testutil.FailErr(t, "seed", store.Store(Credential{Protocol: "https", Host: "bound.example", Username: "alice", Password: "secret"}))
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)

	// Another process running the served git binary is not the invocation.
	other := exec.Command("/bin/sleep", "30")
	testutil.FailErr(t, "start another invocation", other.Start())
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	elsewhere, err := Serve(t.Context(), ServeOptions{GitBinary: self, Store: store})
	testutil.FailErr(t, "serve", err)
	defer func() { _ = elsewhere.Close() }()
	testutil.FailErr(t, "bind another invocation", elsewhere.Bind(other.Process.Pid))
	if out := helperChild(t, elsewhere.Path(), "bound.example"); strings.Contains(out, "secret") {
		t.Fatalf("helper outside the bound invocation received a credential: %q", out)
	}
	if err := elsewhere.Bind(os.Getpid()); err == nil {
		t.Fatal("a bound service was rebound")
	}

	// A service never bound answers nobody once the request deadline passes.
	unbound, err := Serve(t.Context(), ServeOptions{GitBinary: self, Store: store})
	testutil.FailErr(t, "serve", err)
	defer func() { _ = unbound.Close() }()
	unbound.deadline = 500 * time.Millisecond
	if out := helperChild(t, unbound.Path(), "bound.example"); strings.Contains(out, "secret") {
		t.Fatalf("unbound service answered: %q", out)
	}

	// A helper that connects before the bind waits for it.
	late, err := Serve(t.Context(), ServeOptions{GitBinary: self, Store: store})
	testutil.FailErr(t, "serve", err)
	defer func() { _ = late.Close() }()
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = late.Bind(os.Getpid())
	}()
	if out := helperChild(t, late.Path(), "bound.example"); !strings.Contains(out, "password=secret") {
		t.Fatalf("helper connecting before the bind = %q", out)
	}

	exited := exec.Command("/bin/true")
	testutil.FailErr(t, "run exited process", exited.Run())
	if err := late.Bind(exited.Process.Pid); err == nil {
		t.Fatal("bound an exited process")
	}
}

func TestServiceAnswersHelperOperationsForVerifiedPeers(t *testing.T) {
	store := developmentVault(t)
	service := serveForSelf(t, store)
	var out bytes.Buffer
	_, err := helperOperation(t, service.Path(), "store", "protocol=https\nhost=example.com\nusername=alice\npassword=secret\n\n")
	testutil.FailErr(t, "store", err)
	got, err := helperOperation(t, service.Path(), "get", "protocol=https\nhost=example.com\n\n")
	testutil.FailErr(t, "get", err)
	if !strings.Contains(got, "username=alice\npassword=secret\n\n") {
		t.Fatalf("get output = %q", got)
	}
	_, err = helperOperation(t, service.Path(), "erase", "protocol=https\nhost=example.com\n")
	testutil.FailErr(t, "erase", err)
	got, err = helperOperation(t, service.Path(), "get", "protocol=https\nhost=example.com\n\n")
	testutil.FailErr(t, "get after erase", err)
	if strings.Contains(got, "password") {
		t.Fatalf("erased credential served: %q", got)
	}
	// The service's own process is not beneath the invocation it serves.
	if err := Forward("get", service.Path(), strings.NewReader("protocol=https\nhost=example.com\n\n"), &out); err == nil && out.Len() != 0 {
		t.Fatalf("the serving process itself was answered: %q", out.String())
	}
	if err := Forward("steal", service.Path(), strings.NewReader(""), &out); err == nil {
		t.Fatal("unknown operation was forwarded")
	}
	if err := Forward("get", "", strings.NewReader(""), &out); err == nil {
		t.Fatal("missing socket was accepted")
	}
	if err := Forward("get", "relative/credential", strings.NewReader(""), &out); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("relative socket path = %v", err)
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
	testutil.FailErr(t, "bind", service.Bind(os.Getpid()))
	if out := helperChild(t, service.Path(), "example.com"); strings.Contains(out, "secret") {
		t.Fatalf("peer beneath no served git binary received a credential: %q", out)
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
	if output := helperChild(t, service.Path(), "child.example"); !strings.Contains(output, "password=secret") {
		t.Fatalf("child helper output = %q", output)
	}
}

func TestHelperChildProcess(t *testing.T) {
	if os.Getenv("PW_HELPER_CHILD") != "1" {
		// Only a helper child started by another test runs the forward.
		return
	}
	if err := Forward(os.Getenv("PW_HELPER_OPERATION"), os.Getenv(SocketEnv), os.Stdin, os.Stdout); err != nil {
		t.Fatalf("forward: %v", err)
	}
}
