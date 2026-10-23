//go:build linux

package launchbroker_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBrokerRequestsAreBoundedAndComplete(t *testing.T) {
	valid := launchbroker.Request{Version: launchbroker.Version, Argv: []string{"sh"}, Environment: []string{"PATH=/usr/bin"}, Dir: "/"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	detached := valid
	detached.Detach = true
	if err := detached.Validate(); err != nil {
		t.Fatalf("detached request: %v", err)
	}
	for name, mutate := range map[string]func(*launchbroker.Request){
		"version":           func(r *launchbroker.Request) { r.Version = 2 },
		"no program":        func(r *launchbroker.Request) { r.Argv = nil },
		"empty program":     func(r *launchbroker.Request) { r.Argv = []string{""} },
		"relative dir":      func(r *launchbroker.Request) { r.Dir = "tmp" },
		"unclean dir":       func(r *launchbroker.Request) { r.Dir = "/tmp/../etc" },
		"terminal+detach":   func(r *launchbroker.Request) { r.Terminal = true; r.Detach = true },
		"nul argument":      func(r *launchbroker.Request) { r.Argv = []string{"sh", "a\x00b"} },
		"malformed env":     func(r *launchbroker.Request) { r.Environment = []string{"NOEQUALS"} },
		"empty env key":     func(r *launchbroker.Request) { r.Environment = []string{"=x"} },
		"oversized payload": func(r *launchbroker.Request) { r.Argv = []string{strings.Repeat("a", launchbroker.MaxRequest)} },
	} {
		request := valid
		mutate(&request)
		if name == "oversized payload" {
			null, err := os.Open("/dev/null")
			testutil.FailErr(t, "open null", err)
			defer null.Close()
			if _, err := launchbroker.Send(null, request, null, null, null); err == nil {
				t.Fatalf("%s accepted", name)
			}
			continue
		}
		if err := request.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestBrokerRequestDecodingRequiresItsDescriptors(t *testing.T) {
	payload, err := json.Marshal(launchbroker.Request{Version: launchbroker.Version, Argv: []string{"sh"}, Dir: "/"})
	testutil.FailErr(t, "encode", err)
	files := make([]int, 0, launchbroker.RequiredFiles)
	for i := 0; i < launchbroker.RequiredFiles; i++ {
		file, err := os.Open("/dev/null")
		testutil.FailErr(t, "open descriptor", err)
		files = append(files, int(file.Fd()))
		t.Cleanup(func() { _ = file.Close() })
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "pair", err)
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	receive := func(payload []byte, fds []int) (launchbroker.Request, []int, error) {
		t.Helper()
		testutil.FailErr(t, "send", unix.Sendmsg(pair[0], payload, unix.UnixRights(fds...), nil, 0))
		buf, control := make([]byte, 4096), make([]byte, unix.CmsgSpace(8*4))
		n, oobn, _, _, err := unix.Recvmsg(pair[1], buf, control, unix.MSG_CMSG_CLOEXEC)
		testutil.FailErr(t, "receive", err)
		return launchbroker.Decode(buf[:n], control[:oobn])
	}
	request, fds, err := receive(payload, files)
	testutil.FailErr(t, "decode complete request", err)
	if request.Argv[0] != "sh" || len(fds) != launchbroker.RequiredFiles {
		t.Fatalf("decoded %+v with %d descriptors", request, len(fds))
	}
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
	if _, _, err := receive(payload, files[:2]); err == nil {
		t.Fatal("a request without every stream was accepted")
	}
	if _, _, err := receive([]byte(strings.TrimSuffix(string(payload), "}")+`,"extra":1}`), files); err == nil {
		t.Fatal("a request with an unknown field was accepted")
	}
	if _, _, err := receive(append(payload, []byte(" {}")...), files); err == nil {
		t.Fatal("a request with trailing data was accepted")
	}
}

func TestBrokerRequestsBoundTheirExtraDescriptors(t *testing.T) {
	request := launchbroker.Request{Version: launchbroker.Version, Argv: []string{"/opt/painted-wolf-code/bin/pw"}, Dir: "/", ExtraFiles: launchbroker.MaxExtraFiles}
	if err := request.Validate(); err != nil {
		t.Fatalf("bounded extra descriptors: %v", err)
	}
	for name, mutate := range map[string]func(*launchbroker.Request){
		"too many": func(r *launchbroker.Request) { r.ExtraFiles = launchbroker.MaxExtraFiles + 1 },
		"negative": func(r *launchbroker.Request) { r.ExtraFiles = -1 },
	} {
		changed := request
		mutate(&changed)
		if err := changed.Validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestSendDeliversTheStreamsAndExtrasInOrder(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "broker pair", err)
	client, server := os.NewFile(uintptr(pair[0]), "client"), os.NewFile(uintptr(pair[1]), "server")
	defer func() { _ = client.Close(); _ = server.Close() }()
	if _, err := launchbroker.CheckSocket(int(client.Fd())); err != nil {
		t.Fatalf("a seqpacket pair is a broker socket: %v", err)
	}
	streams := make([]*os.File, 5)
	for i := range streams {
		read, write, err := os.Pipe()
		testutil.FailErr(t, "stream", err)
		defer func() { _ = read.Close(); _ = write.Close() }()
		streams[i] = write
	}
	channel, err := launchbroker.Send(client, launchbroker.Request{Argv: []string{"sh"}, Dir: "/"}, streams[0], streams[1], streams[2], streams[3], streams[4])
	testutil.FailErr(t, "send", err)
	defer func() { _ = channel.Close() }()
	buf, control := make([]byte, 4096), make([]byte, launchbroker.ControlSpace())
	n, oobn, _, _, err := unix.Recvmsg(int(server.Fd()), buf, control, unix.MSG_CMSG_CLOEXEC)
	testutil.FailErr(t, "receive", err)
	request, fds, err := launchbroker.Decode(buf[:n], control[:oobn])
	testutil.FailErr(t, "decode", err)
	if request.ExtraFiles != 2 || len(fds) != 6 {
		t.Fatalf("request %+v with %d descriptors", request, len(fds))
	}
	for i, fd := range fds[1:] {
		var got, want unix.Stat_t
		testutil.FailErr(t, "stat received", unix.Fstat(fd, &got))
		testutil.FailErr(t, "stat sent", unix.Fstat(int(streams[i].Fd()), &want))
		if got.Ino != want.Ino {
			t.Fatalf("descriptor %d is not stream %d", fd, i)
		}
	}
	launchbroker.SendEvent(fds[0], launchbroker.Event{Started: 42})
	event, err := launchbroker.ReadEvent(channel)
	if err != nil || event.Started != 42 {
		t.Fatalf("event %+v %v", event, err)
	}
	for _, fd := range fds {
		_ = unix.Close(fd)
	}
	pipe, _, err := os.Pipe()
	testutil.FailErr(t, "pipe", err)
	defer func() { _ = pipe.Close() }()
	if _, err := launchbroker.CheckSocket(int(pipe.Fd())); err == nil {
		t.Fatal("a pipe passed as a broker socket")
	}
}
