//go:build linux || darwin

package sshproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/githelper"
	"github.com/lycaon/lycaon/internal/testutil"
)

func echoDialer(t *testing.T, dials *atomic.Int32) DialFunc {
	t.Helper()
	return func(ctx context.Context, ep egressproxy.Endpoint) (DialResult, error) {
		dials.Add(1)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return DialResult{Reply: ReplyDialFailed}, err
		}
		go func() {
			defer func() { _ = ln.Close() }()
			server, err := ln.Accept()
			if err != nil {
				return
			}
			defer func() { _ = server.Close() }()
			_, _ = fmt.Fprintf(server, "dest=%s:%d\n", ep.Host, ep.Port)
			_, _ = io.Copy(server, server)
		}()
		var d net.Dialer
		client, err := d.DialContext(ctx, "tcp", ln.Addr().String())
		if err != nil {
			_ = ln.Close()
			return DialResult{Reply: ReplyDialFailed}, err
		}
		return DialResult{Conn: client, Reply: ReplySuccess, Stage: StageDial, Outcome: OutcomeEstablished}, nil
	}
}

func runHelperChild(t *testing.T, exe, socket, mode, host, port string, stdin string) (string, error) {
	t.Helper()
	cmd := exec.Command(exe, "-test.run", "^TestSSHHelperChildProcess$")
	cmd.Env = append(os.Environ(),
		"PW_SSH_HELPER_CHILD=1",
		"PW_SSH_HELPER_MODE="+mode,
		"PW_SSH_HOST="+host,
		"PW_SSH_PORT="+port,
		SocketEnv+"="+socket,
	)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestServiceStreamsForVerifiedHelperAndFragmentedFrame(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)

	var dials atomic.Int32
	service, err := Serve(t.Context(), ServeOptions{GitBinary: self, Dial: echoDialer(t, &dials)})
	testutil.FailErr(t, "serve", err)
	socketDir := filepath.Dir(service.Path())
	var released atomic.Int32
	service.OnClose(func() { released.Add(1) })
	testutil.FailErr(t, "bind", service.Bind(os.Getpid()))

	out, err := runHelperChild(t, self, service.Path(), "connect", "GitHub.COM.", "22", "SSH-2.0-client\n")
	testutil.FailErr(t, "connect child", err)
	if out != "dest=github.com:22\nSSH-2.0-client\n" {
		t.Fatalf("stream output = %q", out)
	}

	out, err = runHelperChild(t, self, service.Path(), "fragmented", "git.example.com", "2222", "ping\n")
	testutil.FailErr(t, "fragmented child", err)
	if out != "dest=git.example.com:2222\nping\n" {
		t.Fatalf("fragmented output = %q", out)
	}
	if dials.Load() != 2 {
		t.Fatalf("dials = %d, want 2", dials.Load())
	}

	// Serving process itself does not descend from the served Git invocation.
	if conn, _, err := Connect(t.Context(), service.Path(), "github.com", "22"); err == nil {
		_ = conn.Close()
		t.Fatal("serving process itself was accepted")
	}
	if dials.Load() != 2 {
		t.Fatalf("self call dialed upstream: %d", dials.Load())
	}

	testutil.FailErr(t, "close", service.Close())
	testutil.FailErr(t, "idempotent close", service.Close())
	if released.Load() != 1 {
		t.Fatalf("admission release count = %d, want 1", released.Load())
	}
	if _, err := os.Stat(socketDir); !os.IsNotExist(err) {
		t.Fatalf("service directory remained after close: %v", err)
	}
}

func TestServiceBindOrderingFailureAndDuplicateBind(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)

	// Connect-before-bind waits for Bind and then completes.
	var dials atomic.Int32
	accepted := make(chan struct{}, 1)
	late, err := serve(t.Context(), ServeOptions{GitBinary: self, Dial: echoDialer(t, &dials)}, func(s *Service) {
		s.accepted = func() {
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	})
	testutil.FailErr(t, "serve late", err)
	defer func() { _ = late.Close() }()
	go func() {
		<-accepted
		_ = late.Bind(os.Getpid())
	}()
	out, err := runHelperChild(t, self, late.Path(), "connect", "github.com", "22", "ok\n")
	testutil.FailErr(t, "connect before bind", err)
	if !strings.Contains(out, "dest=github.com:22\nok\n") {
		t.Fatalf("connect-before-bind output = %q", out)
	}

	// Duplicate bind is refused without altering the bound process.
	other := exec.Command("/bin/sleep", "30")
	testutil.FailErr(t, "start sleep", other.Start())
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	if err := late.Bind(other.Process.Pid); err == nil {
		t.Fatal("duplicate bind succeeded")
	}
	if late.BindError() != nil {
		t.Fatalf("duplicate bind polluted BindError: %v", late.BindError())
	}

	// Failed bind wakes waiting handshakes immediately and records BindError.
	exited := exec.Command("true")
	testutil.FailErr(t, "run exited", exited.Run())
	failAccepted := make(chan struct{}, 1)
	failedBind, err := serve(t.Context(), ServeOptions{GitBinary: self, Dial: echoDialer(t, &dials)}, func(s *Service) {
		s.accepted = func() {
			select {
			case failAccepted <- struct{}{}:
			default:
			}
		}
	})
	testutil.FailErr(t, "serve failed bind", err)
	defer func() { _ = failedBind.Close() }()
	go func() {
		<-failAccepted
		_ = failedBind.Bind(exited.Process.Pid)
	}()
	start := time.Now()
	if _, err := runHelperChild(t, self, failedBind.Path(), "connect", "github.com", "22", ""); err == nil {
		t.Fatal("helper succeeded after failed bind")
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("failed bind did not wake waiting helper promptly: %v", elapsed)
	}
	if failedBind.BindError() == nil {
		t.Fatal("failed bind recorded no BindError")
	}
	if dials.Load() != 1 {
		t.Fatalf("failed bind dialed upstream: %d", dials.Load())
	}
}

func TestServiceRejectsWrongHelperWrongInvocationAndTwoSessions(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)

	// Copy the helper executable so its inode differs while remaining a child of this process.
	foreignHelper := filepath.Join(t.TempDir(), "foreign-helper")
	rawExe, err := os.ReadFile(self)
	testutil.FailErr(t, "read test binary", err)
	testutil.FailErr(t, "write foreign helper", os.WriteFile(foreignHelper, rawExe, 0o755))

	var dialsA, dialsB atomic.Int32
	serviceA, err := Serve(t.Context(), ServeOptions{GitBinary: self, Dial: echoDialer(t, &dialsA)})
	testutil.FailErr(t, "serve A", err)
	defer func() { _ = serviceA.Close() }()
	testutil.FailErr(t, "bind A", serviceA.Bind(os.Getpid()))

	if _, err := runHelperChild(t, foreignHelper, serviceA.Path(), "connect", "github.com", "22", ""); err == nil {
		t.Fatal("foreign helper binary was accepted")
	}
	if dialsA.Load() != 0 {
		t.Fatalf("foreign helper triggered %d dials", dialsA.Load())
	}

	// Second simultaneous session bound to a different live invocation rejects our helper.
	other := exec.Command("/bin/sleep", "30")
	testutil.FailErr(t, "start other invocation", other.Start())
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })

	serviceB, err := Serve(t.Context(), ServeOptions{GitBinary: self, Dial: echoDialer(t, &dialsB)})
	testutil.FailErr(t, "serve B", err)
	defer func() { _ = serviceB.Close() }()
	testutil.FailErr(t, "bind B", serviceB.Bind(other.Process.Pid))

	if _, err := runHelperChild(t, self, serviceB.Path(), "connect", "github.com", "22", ""); err == nil {
		t.Fatal("cross-session helper was accepted by service B")
	}
	if dialsB.Load() != 0 {
		t.Fatalf("cross-session helper triggered %d dials on B", dialsB.Load())
	}
	if got := serviceB.Attempts(); len(got) != 0 {
		t.Fatalf("unverified helper recorded attempts on B: %+v", got)
	}
	if got := serviceA.Attempts(); len(got) != 0 {
		t.Fatalf("foreign helper recorded attempts on A: %+v", got)
	}

	// Meanwhile service A still serves its own verified helper cleanly.
	serviceA.SetExecutionID("exec-a")
	out, err := runHelperChild(t, self, serviceA.Path(), "connect", "github.com", "22", "hello\n")
	testutil.FailErr(t, "connect A", err)
	if out != "dest=github.com:22\nhello\n" || dialsA.Load() != 1 {
		t.Fatalf("service A output = %q, dials = %d", out, dialsA.Load())
	}
	if got := serviceA.Attempts(); len(got) != 1 || got[0].ExecutionID != "exec-a" || got[0].Outcome != OutcomeEstablished || got[0].Destination != "github.com:22" {
		t.Fatalf("service A attempts = %+v", got)
	}
}

func TestServiceFrameValidationReplyCodesAndConnectionLimit(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)

	var dials atomic.Int32
	var nextCode atomic.Uint32
	service, err := Serve(t.Context(), ServeOptions{
		GitBinary:   self,
		ExecutionID: "exec-validate",
		Dial: func(_ context.Context, _ egressproxy.Endpoint) (DialResult, error) {
			dials.Add(1)
			code := ReplyCode(nextCode.Load())
			return DialResult{Reply: code, PolicyReason: "test_policy", ErrorClass: "connection_refused"}, errors.New("refused")
		},
	})
	testutil.FailErr(t, "serve", err)
	defer func() { _ = service.Close() }()
	testutil.FailErr(t, "bind", service.Bind(os.Getpid()))

	for _, mode := range []string{"bad-version", "truncated-header", "truncated-host"} {
		if out, err := runHelperChild(t, self, service.Path(), mode, "github.com", "22", ""); err == nil {
			t.Fatalf("%s unexpectedly succeeded: %q", mode, out)
		}
	}
	if got := service.Attempts(); len(got) != 0 {
		t.Fatalf("aborted frames recorded attempts: %+v", got)
	}
	for _, mode := range []string{"bad-opcode", "zero-host", "oversize-host", "zero-port", "bad-host"} {
		out, err := runHelperChild(t, self, service.Path(), mode, "github.com", "22", "")
		if err == nil || !strings.Contains(out, "code=4") {
			t.Fatalf("%s = (%q, %v), want ReplyInvalidRequest (4)", mode, out, err)
		}
	}
	if dials.Load() != 0 {
		t.Fatalf("invalid frames dialed upstream %d times", dials.Load())
	}

	for _, code := range []ReplyCode{ReplyLeaseEnded, ReplyEndpointDenied, ReplyDialFailed} {
		nextCode.Store(uint32(code))
		out, err := runHelperChild(t, self, service.Path(), "connect", "github.com", "22", "")
		if err == nil || !strings.Contains(out, fmt.Sprintf("code=%d", code)) {
			t.Fatalf("reply code %d = (%q, %v)", code, out, err)
		}
	}
	if got := service.Attempts(); len(got) != 8 || got[5].Outcome != OutcomeLeaseEnded || got[6].Outcome != OutcomeEndpointDenied || got[6].PolicyReason != "test_policy" || got[7].Outcome != OutcomeDialFailed || got[7].ErrorClass != "connection_refused" {
		t.Fatalf("service attempts = %+v", got)
	}

	// Hold 16 active connections from a verified child helper, assert the 17th gets ReplyServiceBusy (5),
	// then release one connection and verify the freed slot admits the next request.
	gate := make(chan struct{})
	entered := make(chan struct{}, MaxActiveConnections)
	slotReleased := make(chan struct{}, MaxActiveConnections)
	busyService, err := Serve(t.Context(), ServeOptions{
		GitBinary: self,
		Dial: func(ctx context.Context, ep egressproxy.Endpoint) (DialResult, error) {
			entered <- struct{}{}
			select {
			case <-gate:
			case <-ctx.Done():
				slotReleased <- struct{}{}
				return DialResult{Reply: ReplyLeaseEnded}, ctx.Err()
			}
			return echoDialer(t, &dials)(ctx, ep)
		},
	})
	testutil.FailErr(t, "serve busy", err)
	defer func() { _ = busyService.Close() }()
	testutil.FailErr(t, "bind busy", busyService.Bind(os.Getpid()))

	holdCmd := exec.Command(self, "-test.run", "^TestSSHHelperChildProcess$")
	holdCmd.Env = append(os.Environ(),
		"PW_SSH_HELPER_CHILD=1",
		"PW_SSH_HELPER_MODE=hold-slots",
		"PW_SSH_HOST=github.com",
		"PW_SSH_PORT=22",
		SocketEnv+"="+busyService.Path(),
	)
	holdIn, err := holdCmd.StdinPipe()
	testutil.FailErr(t, "hold stdin pipe", err)
	testutil.FailErr(t, "start hold child", holdCmd.Start())
	t.Cleanup(func() {
		_ = holdIn.Close()
		_ = holdCmd.Process.Kill()
		_ = holdCmd.Wait()
	})

	for range MaxActiveConnections {
		<-entered
	}

	out, err := runHelperChild(t, self, busyService.Path(), "connect", "github.com", "22", "")
	if err == nil || !strings.Contains(out, fmt.Sprintf("code=%d", ReplyServiceBusy)) {
		t.Fatalf("17th connection = (%q, %v), want ReplyServiceBusy (5)", out, err)
	}
	if got := busyService.Attempts(); len(got) != 0 {
		t.Fatalf("over-limit ReplyServiceBusy recorded verified attempts before completion: %+v", got)
	}

	// Ask the child to drop one held connection; disconnect cancels its pending dial and frees a slot.
	_, err = io.WriteString(holdIn, "drop-one\n")
	testutil.FailErr(t, "signal drop-one", err)
	<-slotReleased
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		busyService.mu.Lock()
		n := busyService.active
		busyService.mu.Unlock()
		if n == MaxActiveConnections-1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	close(gate)
	_, err = runHelperChild(t, self, busyService.Path(), "connect", "github.com", "22", "resumed\n")
	testutil.FailErr(t, "16th slot after release", err)
	_ = holdIn.Close()
	_ = holdCmd.Wait()
}

func TestServiceClosureDuringSetupAndActiveStream(t *testing.T) {
	self, err := os.Executable()
	testutil.FailErr(t, "resolve test binary", err)

	dialEntered := make(chan struct{})
	dialCanceled := make(chan struct{})
	service, err := Serve(t.Context(), ServeOptions{
		GitBinary: self,
		Dial: func(ctx context.Context, _ egressproxy.Endpoint) (DialResult, error) {
			close(dialEntered)
			<-ctx.Done()
			close(dialCanceled)
			return DialResult{Reply: ReplyLeaseEnded}, ctx.Err()
		},
	})
	testutil.FailErr(t, "serve", err)
	testutil.FailErr(t, "bind", service.Bind(os.Getpid()))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = runHelperChild(t, self, service.Path(), "connect", "github.com", "22", "")
	}()
	<-dialEntered
	testutil.FailErr(t, "close while dial pending", service.Close())
	<-dialCanceled
	wg.Wait()
}

func TestSSHHelperChildProcess(t *testing.T) {
	if os.Getenv("PW_SSH_HELPER_CHILD") != "1" {
		return
	}
	os.Exit(runSSHHelperChild())
}

func runSSHHelperChild() int {
	socket := os.Getenv(SocketEnv)
	mode := os.Getenv("PW_SSH_HELPER_MODE")
	host := os.Getenv("PW_SSH_HOST")
	portStr := os.Getenv("PW_SSH_PORT")

	if mode == "hold-slots" {
		portNum, _ := strconv.ParseUint(portStr, 10, 16)
		frame, err := EncodeConnectRequest(host, uint16(portNum))
		if err != nil {
			return 1
		}
		var conns []*net.UnixConn
		for range MaxActiveConnections {
			c, err := githelper.Dial(socket)
			if err != nil {
				return 1
			}
			if err := githelper.WriteIdentified(c, frame); err != nil {
				return 1
			}
			conns = append(conns, c)
		}
		buf := make([]byte, 16)
		_, _ = os.Stdin.Read(buf)
		_ = conns[0].Close()
		_, _ = io.Copy(io.Discard, os.Stdin)
		for _, c := range conns[1:] {
			_ = c.Close()
		}
		return 0
	}

	if mode == "connect" {
		conn, code, err := Connect(context.Background(), socket, host, portStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "code=%d err=%v\n", code, err)
			return 1
		}
		defer func() { _ = conn.Close() }()
		inData, _ := io.ReadAll(os.Stdin)
		if len(inData) > 0 {
			_, _ = conn.Write(inData)
		}
		_ = conn.CloseWrite()
		out, err := io.ReadAll(conn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read: %v\n", err)
			return 1
		}
		_, _ = os.Stdout.Write(out)
		return 0
	}

	conn, err := githelper.Dial(socket)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial: %v\n", err)
		return 1
	}
	defer func() { _ = conn.Close() }()

	portNum, _ := strconv.ParseUint(portStr, 10, 16)
	switch mode {
	case "fragmented":
		frame, err := EncodeConnectRequest(host, uint16(portNum))
		if err != nil {
			return 1
		}
		if err := githelper.WriteIdentified(conn, frame[:2]); err != nil {
			return 1
		}
		for i := 2; i < len(frame); i += 3 {
			end := min(i+3, len(frame))
			if _, err := conn.Write(frame[i:end]); err != nil {
				return 1
			}
		}
		var reply [1]byte
		if _, err := io.ReadFull(conn, reply[:]); err != nil || ReplyCode(reply[0]) != ReplySuccess {
			return 1
		}
		inData, _ := io.ReadAll(os.Stdin)
		_, _ = conn.Write(inData)
		_ = conn.CloseWrite()
		out, _ := io.ReadAll(conn)
		_, _ = os.Stdout.Write(out)
		return 0
	case "bad-version":
		_ = githelper.WriteIdentified(conn, []byte{99, OpcodeConnect, 0, 1, 0, 22, 'a'})
	case "truncated-header":
		_ = githelper.WriteIdentified(conn, []byte{ProtocolVersion, OpcodeConnect, 0})
		_ = conn.CloseWrite()
	case "truncated-host":
		_ = githelper.WriteIdentified(conn, []byte{ProtocolVersion, OpcodeConnect, 0, 10, 0, 22, 'a', 'b'})
		_ = conn.CloseWrite()
	case "bad-opcode":
		_ = githelper.WriteIdentified(conn, []byte{ProtocolVersion, 9, 0, 1, 0, 22, 'a'})
	case "zero-host":
		_ = githelper.WriteIdentified(conn, []byte{ProtocolVersion, OpcodeConnect, 0, 0, 0, 22})
	case "oversize-host":
		var hdr [headerBytes]byte
		hdr[0], hdr[1] = ProtocolVersion, OpcodeConnect
		binary.BigEndian.PutUint16(hdr[2:4], 300)
		binary.BigEndian.PutUint16(hdr[4:6], 22)
		_ = githelper.WriteIdentified(conn, append(hdr[:], bytes.Repeat([]byte("a"), 300)...))
	case "zero-port":
		_ = githelper.WriteIdentified(conn, []byte{ProtocolVersion, OpcodeConnect, 0, 10, 0, 0, 'g', 'i', 't', 'h', 'u', 'b', '.', 'c', 'o', 'm'})
	case "bad-host":
		_ = githelper.WriteIdentified(conn, []byte{ProtocolVersion, OpcodeConnect, 0, 9, 0, 22, 'b', 'a', 'd', '.', '.', 'h', 'o', 's', 't'})
	}
	var reply [1]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		fmt.Fprintf(os.Stderr, "eof: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "code=%d\n", reply[0])
	return 1
}
