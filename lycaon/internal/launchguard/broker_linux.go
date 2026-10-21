//go:build linux

package launchguard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// The broker starts programs that must not run inside the protected
// namespace: approved host execution, terminals and desktop handlers. It runs
// in the supervisor, as the caller in the initial namespaces, so whatever it
// starts has exactly the authority any program the caller runs has, and
// none over the protected processes. Only protected processes hold its
// socket. Each request carries its own call channel; the broker reports the
// start and the exit there, and ends the program if the channel closes first.

const (
	brokerVersion     = 1
	maxBrokerRequest  = 256 << 10
	brokerRequestFDs  = 4 // channel, stdin, stdout, stderr
	brokerControlSize = 64
)

// BrokerRequest asks the broker to start one program.
type BrokerRequest struct {
	Version     int      `json:"version"`
	Argv        []string `json:"argv"`
	Environment []string `json:"environment"`
	Dir         string   `json:"dir"`
	// Terminal makes the program a session leader whose controlling terminal
	// is its standard input.
	Terminal bool `json:"terminal,omitempty"`
}

// BrokerEvent is one report on a call channel.
type BrokerEvent struct {
	Started int    `json:"started,omitempty"`
	Exited  bool   `json:"exited,omitempty"`
	Code    int    `json:"code,omitempty"`
	Signal  int    `json:"signal,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (r BrokerRequest) validate() error {
	if r.Version != brokerVersion {
		return errors.New("unsupported broker request version")
	}
	if len(r.Argv) == 0 || r.Argv[0] == "" {
		return errors.New("broker request names no program")
	}
	if !filepath.IsAbs(r.Dir) || filepath.Clean(r.Dir) != r.Dir {
		return errors.New("broker request directory must be absolute and clean")
	}
	for _, value := range append(append([]string{r.Dir}, r.Argv...), r.Environment...) {
		if strings.ContainsRune(value, 0) {
			return errors.New("broker request holds a NUL byte")
		}
	}
	for _, entry := range r.Environment {
		if key, _, ok := strings.Cut(entry, "="); !ok || key == "" {
			return errors.New("broker request holds a malformed environment entry")
		}
	}
	return nil
}

// decodeBrokerRequest reads one request and its descriptors from a message.
func decodeBrokerRequest(payload, control []byte) (BrokerRequest, []int, error) {
	var fds []int
	messages, err := unix.ParseSocketControlMessage(control)
	if err != nil {
		return BrokerRequest{}, nil, err
	}
	for _, message := range messages {
		rights, err := unix.ParseUnixRights(&message)
		if err != nil {
			continue
		}
		fds = append(fds, rights...)
	}
	closeAll := func() {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
	}
	if len(fds) != brokerRequestFDs {
		closeAll()
		return BrokerRequest{}, nil, fmt.Errorf("broker request carries %d descriptors, want %d", len(fds), brokerRequestFDs)
	}
	var request BrokerRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		closeAll()
		return BrokerRequest{}, nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		closeAll()
		return BrokerRequest{}, nil, errors.New("broker request has trailing data")
	}
	if err := request.validate(); err != nil {
		closeAll()
		return BrokerRequest{}, nil, err
	}
	for _, fd := range fds {
		unix.CloseOnExec(fd)
	}
	return request, fds, nil
}

// SendBrokerRequest asks the broker on socket to start a program with the
// given standard streams. It returns the call channel the broker reports on;
// closing it ends the program.
func SendBrokerRequest(socket *os.File, request BrokerRequest, stdin, stdout, stderr *os.File) (*os.File, error) {
	request.Version = brokerVersion
	if err := request.validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(payload) > maxBrokerRequest {
		return nil, errors.New("broker request exceeds its bound")
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	channel := os.NewFile(uintptr(pair[0]), "broker-call")
	rights := unix.UnixRights(pair[1], int(stdin.Fd()), int(stdout.Fd()), int(stderr.Fd()))
	err = unix.Sendmsg(int(socket.Fd()), payload, rights, nil, 0)
	_ = unix.Close(pair[1])
	if err != nil {
		_ = channel.Close()
		return nil, err
	}
	return channel, nil
}

// ReadBrokerEvent reads the next report from a call channel.
func ReadBrokerEvent(channel *os.File) (BrokerEvent, error) {
	buf := make([]byte, 4096)
	n, err := channel.Read(buf)
	if err != nil {
		return BrokerEvent{}, err
	}
	var event BrokerEvent
	if err := json.Unmarshal(buf[:n], &event); err != nil {
		return BrokerEvent{}, err
	}
	return event, nil
}

func sendBrokerEvent(channel int, event BrokerEvent) {
	raw, err := json.Marshal(event)
	if err == nil {
		_, _ = unix.Write(channel, raw)
	}
}

// brokerCall is one started program the supervisor's reaper reports on.
type brokerCall struct {
	channel int
	pid     int
	// abandoned marks a call whose caller closed the channel first.
	abandoned bool
}

// startBrokered forks the requested program with the request's streams.
func startBrokered(request BrokerRequest, fds []int) (int, error) {
	path := request.Argv[0]
	if !strings.Contains(path, "/") {
		resolved, err := lookPath(path, request.Environment)
		if err != nil {
			return 0, err
		}
		path = resolved
	}
	attributes := &syscall.ProcAttr{
		Dir:   request.Dir,
		Env:   request.Environment,
		Files: []uintptr{uintptr(fds[1]), uintptr(fds[2]), uintptr(fds[3])},
		Sys:   &syscall.SysProcAttr{Setsid: true, Setctty: request.Terminal, Ctty: 0},
	}
	if !request.Terminal {
		attributes.Sys.Setctty = false
	}
	return syscall.ForkExec(path, request.Argv, attributes)
}

// lookPath resolves a program name through the request's own PATH.
func lookPath(name string, environment []string) (string, error) {
	path := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s: not found on the request's PATH", name)
}
