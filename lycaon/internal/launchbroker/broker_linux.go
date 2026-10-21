//go:build linux

// Package launchbroker is the protocol between protected processes and the
// protected launch supervisor's broker, which starts programs that must not
// run inside the protected user namespace. A request is one SOCK_SEQPACKET
// message: a JSON Request with descriptors attached in order — its call
// channel, standard input, output and error, then Request.ExtraFiles more,
// which the program receives from descriptor 3. The broker reports Events on
// the call channel and ends the program's session if the channel closes before
// the program exits. The broker runs in the launch's own mount namespace, so a
// path under the logical installation root names the running generation.
package launchbroker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	// Version is the only request shape the broker accepts.
	Version = 1
	// MaxRequest bounds one request's JSON.
	MaxRequest = 256 << 10
	// RequiredFiles are the call channel and the three standard streams.
	RequiredFiles = 4
	// MaxExtraFiles bounds the descriptors a program inherits beyond those.
	MaxExtraFiles = 16
	// ControlSpace sizes a receive buffer for one request's descriptors.
	controlSlack = 64
)

// ControlSpace is the ancillary buffer a receiver needs for one request.
func ControlSpace() int {
	return unix.CmsgSpace((RequiredFiles+MaxExtraFiles)*4) + controlSlack
}

// Request asks the broker to start one program.
type Request struct {
	Version     int      `json:"version"`
	Argv        []string `json:"argv"`
	Environment []string `json:"environment"`
	Dir         string   `json:"dir"`
	// Terminal makes the program a session leader whose controlling terminal
	// is its standard input.
	Terminal bool `json:"terminal,omitempty"`
	// ExtraFiles counts the descriptors after the standard streams.
	ExtraFiles int `json:"extra_files,omitempty"`
}

// Event is one report on a call channel.
type Event struct {
	Started int    `json:"started,omitempty"`
	Exited  bool   `json:"exited,omitempty"`
	Code    int    `json:"code,omitempty"`
	Signal  int    `json:"signal,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Validate checks a request's shape.
func (r Request) Validate() error {
	if r.Version != Version {
		return errors.New("unsupported broker request version")
	}
	if len(r.Argv) == 0 || r.Argv[0] == "" {
		return errors.New("broker request names no program")
	}
	if !filepath.IsAbs(r.Dir) || filepath.Clean(r.Dir) != r.Dir {
		return errors.New("broker request directory must be absolute and clean")
	}
	if r.ExtraFiles < 0 || r.ExtraFiles > MaxExtraFiles {
		return fmt.Errorf("broker request carries %d extra descriptors, at most %d", r.ExtraFiles, MaxExtraFiles)
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

// Decode reads one request and its descriptors from a received message. The
// descriptors are close-on-exec and owned by the caller on success; every
// one is closed on failure.
func Decode(payload, control []byte) (Request, []int, error) {
	fds := Rights(control)
	fail := func(err error) (Request, []int, error) {
		for _, fd := range fds {
			_ = unix.Close(fd)
		}
		return Request{}, nil, err
	}
	var request Request
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return fail(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fail(errors.New("broker request has trailing data"))
	}
	if err := request.Validate(); err != nil {
		return fail(err)
	}
	if len(fds) != RequiredFiles+request.ExtraFiles {
		return fail(fmt.Errorf("broker request carries %d descriptors, want %d", len(fds), RequiredFiles+request.ExtraFiles))
	}
	for _, fd := range fds {
		unix.CloseOnExec(fd)
	}
	return request, fds, nil
}

// Rights lists the descriptors in a control message.
func Rights(control []byte) []int {
	var fds []int
	messages, err := unix.ParseSocketControlMessage(control)
	if err != nil {
		return nil
	}
	for _, message := range messages {
		if rights, err := unix.ParseUnixRights(&message); err == nil {
			fds = append(fds, rights...)
		}
	}
	return fds
}

// Send asks the broker on socket to start a program with the given standard
// streams and extra descriptors. It returns the call channel the broker
// reports on; closing it ends the program.
func Send(socket *os.File, request Request, stdin, stdout, stderr *os.File, extra ...*os.File) (*os.File, error) {
	request.Version = Version
	request.ExtraFiles = len(extra)
	if err := request.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(payload) > MaxRequest {
		return nil, errors.New("broker request exceeds its bound")
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	channel := os.NewFile(uintptr(pair[0]), "broker-call")
	fds := []int{pair[1], int(stdin.Fd()), int(stdout.Fd()), int(stderr.Fd())}
	for _, file := range extra {
		fds = append(fds, int(file.Fd()))
	}
	err = unix.Sendmsg(int(socket.Fd()), payload, unix.UnixRights(fds...), nil, 0)
	_ = unix.Close(pair[1])
	if err != nil {
		_ = channel.Close()
		return nil, err
	}
	return channel, nil
}

// ReadEvent reads the next report from a call channel.
func ReadEvent(channel *os.File) (Event, error) {
	buf := make([]byte, 4096)
	n, err := channel.Read(buf)
	if err != nil {
		return Event{}, err
	}
	var event Event
	if err := json.Unmarshal(buf[:n], &event); err != nil {
		return Event{}, err
	}
	return event, nil
}

// SendEvent writes one report on a call channel descriptor.
func SendEvent(channel int, event Event) {
	if raw, err := json.Marshal(event); err == nil {
		_, _ = unix.Write(channel, raw)
	}
}

// CheckSocket requires a broker socket: a SOCK_SEQPACKET Unix socket. It
// returns the kernel's record of the peer that created the other end.
func CheckSocket(fd int) (*unix.Ucred, error) {
	domain, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil || domain != unix.AF_UNIX {
		return nil, errors.New("broker descriptor is not a Unix socket")
	}
	kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
	if err != nil || kind != unix.SOCK_SEQPACKET {
		return nil, errors.New("broker descriptor is not a seqpacket socket")
	}
	peer, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || peer.Pid <= 0 {
		return nil, errors.New("broker socket has no peer")
	}
	return peer, nil
}
