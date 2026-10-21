//go:build linux

package launchguard

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBrokerRequestsAreBoundedAndComplete(t *testing.T) {
	valid := BrokerRequest{Version: brokerVersion, Argv: []string{"sh"}, Environment: []string{"PATH=/usr/bin"}, Dir: "/"}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	for name, mutate := range map[string]func(*BrokerRequest){
		"version":       func(r *BrokerRequest) { r.Version = 2 },
		"no program":    func(r *BrokerRequest) { r.Argv = nil },
		"empty program": func(r *BrokerRequest) { r.Argv = []string{""} },
		"relative dir":  func(r *BrokerRequest) { r.Dir = "tmp" },
		"unclean dir":   func(r *BrokerRequest) { r.Dir = "/tmp/../etc" },
		"nul argument":  func(r *BrokerRequest) { r.Argv = []string{"sh", "a\x00b"} },
		"malformed env": func(r *BrokerRequest) { r.Environment = []string{"NOEQUALS"} },
		"empty env key": func(r *BrokerRequest) { r.Environment = []string{"=x"} },
	} {
		request := valid
		mutate(&request)
		if err := request.validate(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestBrokerRequestDecodingRequiresItsDescriptors(t *testing.T) {
	payload, err := json.Marshal(BrokerRequest{Version: brokerVersion, Argv: []string{"sh"}, Dir: "/"})
	testutil.FailErr(t, "encode", err)
	files := make([]int, 0, brokerRequestFDs)
	for i := 0; i < brokerRequestFDs; i++ {
		file, err := os.Open("/dev/null")
		testutil.FailErr(t, "open descriptor", err)
		files = append(files, int(file.Fd()))
		t.Cleanup(func() { _ = file.Close() })
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "pair", err)
	defer func() { _ = unix.Close(pair[0]); _ = unix.Close(pair[1]) }()
	receive := func(payload []byte, fds []int) (BrokerRequest, []int, error) {
		t.Helper()
		testutil.FailErr(t, "send", unix.Sendmsg(pair[0], payload, unix.UnixRights(fds...), nil, 0))
		buf, control := make([]byte, 4096), make([]byte, unix.CmsgSpace(8*4))
		n, oobn, _, _, err := unix.Recvmsg(pair[1], buf, control, unix.MSG_CMSG_CLOEXEC)
		testutil.FailErr(t, "receive", err)
		return decodeBrokerRequest(buf[:n], control[:oobn])
	}
	request, fds, err := receive(payload, files)
	testutil.FailErr(t, "decode complete request", err)
	if request.Argv[0] != "sh" || len(fds) != brokerRequestFDs {
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

func TestBrokerLooksProgramsUpOnTheRequestPath(t *testing.T) {
	path, err := lookPath("sh", []string{"PATH=relative:/usr/bin:/bin"})
	testutil.FailErr(t, "resolve sh", err)
	if !strings.HasSuffix(path, "/sh") || !strings.HasPrefix(path, "/") {
		t.Fatalf("resolved %q", path)
	}
	if _, err := lookPath("sh", []string{"PATH=relative"}); err == nil {
		t.Fatal("a relative PATH entry resolved a program")
	}
}
