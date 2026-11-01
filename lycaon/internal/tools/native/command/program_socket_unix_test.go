//go:build darwin || linux

package command_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
)

func socketProgramFixture(t *testing.T) (*programFixture, string, *atomic.Int32) {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "pw-program-socket-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "create program socket fixture", err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(t.TempDir(), strings.Repeat("long-state-", 15)))
	path := filepath.Join(root, "service")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen for program socket fixture", err)
	count := &atomic.Int32{}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		_, _ = io.WriteString(w, "fixture-response")
	})}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	f := newProgramFixture(t)
	runtime := approvalstate.NewSocketCapabilityRuntime()
	t.Cleanup(runtime.Close)
	f.Runtime.Executor.Capabilities.SetSocketCapabilityRuntime(runtime)
	f.Runtime.Executor.Capabilities.SetApprovalsDisabled(func(string) bool { return true })
	return f, path, count
}

func socketProgramArgs(program, path string) map[string]any {
	return map[string]any{"command": program, "capability_request": map[string]any{"socket_paths": []any{path}}}
}

func TestProgramExternalSocketReferencesUseThePreparedLaunchDoor(t *testing.T) {
	f, path, count := socketProgramFixture(t)
	reference := "{{paintedwolf-socket:" + url.PathEscape(path) + "}}"
	curl, err := exec.LookPath("curl")
	testutil.FailErr(t, "locate real socket client", err)
	client := "external -- " + strconv.Quote(curl) + " -q --silent --show-error --max-time 5 --unix-socket "
	for _, program := range []string{
		client + strconv.Quote(reference) + " http://fixture.invalid/",
		"export SERVICE_SOCKET=" + strconv.Quote(reference) + "; " + client + "\"$SERVICE_SOCKET\" http://fixture.invalid/",
	} {
		out, err := f.CallArgs(t, t.Context(), socketProgramArgs(program, path))
		testutil.FailErr(t, "run socket reference through command program", err)
		if result := decodeOutcome(t, out); result.Status != 0 || result.Stdout != "fixture-response" {
			t.Fatalf("bound endpoint did not reach its service: %s", out)
		}
	}
	if got := count.Load(); got != 2 {
		t.Fatalf("service requests=%d, want 2", got)
	}
	printf, err := exec.LookPath("printf")
	testutil.FailErr(t, "locate literal argument fixture", err)
	ordinary := "external -- " + strconv.Quote(printf) + " %s " + strconv.Quote(path)
	out, err := f.CallArgs(t, t.Context(), socketProgramArgs(ordinary, path))
	testutil.FailErr(t, "preserve ordinary argument with approved socket", err)
	if result := decodeOutcome(t, out); result.Status != 0 || result.Stdout != path {
		t.Fatalf("ordinary socket operand was rewritten: %s", out)
	}
	out, err = f.CallArgs(t, t.Context(), socketProgramArgs(client+strconv.Quote(path)+" http://fixture.invalid/", path))
	testutil.FailErr(t, "observe raw pathname refusal", err)
	result := decodeOutcome(t, out)
	wantRequests := int32(2)
	if runtime.GOOS == "darwin" {
		if result.Status == 0 {
			t.Fatalf("raw pathname inherited process-bound authority: %s", out)
		}
	} else {
		wantRequests = 3
		if result.Status != 0 || result.Stdout != "fixture-response" {
			t.Fatalf("Linux retained pathname contract regressed: %s", out)
		}
	}
	if got := count.Load(); got != wantRequests {
		t.Fatalf("service requests=%d, want %d", got, wantRequests)
	}
}

func TestProgramSocketBackgroundFixture(t *testing.T) {
	fifo := os.Getenv("PW_SOCKET_TEST_FIFO")
	if fifo == "" {
		return
	}
	gate, err := os.Open(fifo)
	testutil.FailErr(t, "open controlled background connection gate", err)
	var released [1]byte
	_, err = io.ReadFull(gate, released[:])
	testutil.FailErr(t, "wait for foreground to return", err)
	_ = gate.Close()
	conn, err := net.Dial("unix", os.Getenv("SERVICE_SOCKET"))
	testutil.FailErr(t, "connect after foreground returned", err)
	defer conn.Close()
	_, err = io.WriteString(conn, "GET / HTTP/1.0\r\nHost: fixture.invalid\r\n\r\n")
	testutil.FailErr(t, "write background fixture request", err)
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	testutil.FailErr(t, "read background fixture response", err)
	defer response.Body.Close()
	_, err = io.Copy(os.Stdout, response.Body)
	testutil.FailErr(t, "deliver background fixture response", err)
}

func TestProgramSocketEndpointOutlivesTheForegroundCall(t *testing.T) {
	f, path, count := socketProgramFixture(t)
	fifo := filepath.Join(f.Dir, "connection-gate")
	testutil.FailErr(t, "create controlled background gate", syscall.Mkfifo(fifo, 0600))
	gate, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0600)
	testutil.FailErr(t, "hold background gate", err)
	defer gate.Close()
	self, err := os.Executable()
	testutil.FailErr(t, "locate background socket fixture", err)
	reference := "{{paintedwolf-socket:" + url.PathEscape(path) + "}}"
	program := fmt.Sprintf("PW_SOCKET_TEST_FIFO=%s SERVICE_SOCKET=%s external -- %s -test.run=^TestProgramSocketBackgroundFixture$ &", strconv.Quote(fifo), strconv.Quote(reference), strconv.Quote(self))
	_, err = f.CallArgs(t, t.Context(), socketProgramArgs(program, path))
	testutil.FailErr(t, "launch background socket operation", err)
	if count.Load() != 0 {
		t.Fatal("background fixture crossed the unreleased gate")
	}
	_, err = gate.Write([]byte{1})
	testutil.FailErr(t, "release connection after foreground returned", err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	handle := f.registryHandle(t)
	exit, stopped, err := f.Background.Lifecycle.Wait(ctx, f.Session, handle)
	if err != nil || stopped || exit != 0 {
		t.Fatalf("background socket operation exit=%d, stopped=%v, error=%v", exit, stopped, err)
	}
	snapshot, err := f.Background.Snapshot(ctx, f.Session, handle, 4096)
	testutil.FailErr(t, "observe background socket operation", err)
	if snapshot.Failure != nil || !strings.Contains(snapshot.Output, "fixture-response") || count.Load() != 1 {
		t.Fatalf("background socket authority did not survive: snapshot=%+v, service requests=%d", snapshot, count.Load())
	}
}
