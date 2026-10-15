//go:build linux

package linuxsandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// The outer helper serves datagrams inside the sandbox on sockets created
// there. It asks the inner helper for each one over the control socket; the
// inner helper creates it unbound and passes it back. A socket keeps the
// network namespace it was created in, whoever holds it.

// controlMessage is what the outer helper sends the inner helper.
type controlMessage struct {
	Kind   string `json:"kind"`
	Family int    `json:"family,omitempty"`
}

const (
	// controlSocket asks for a datagram socket of Family.
	controlSocket = "socket"
	// controlReady says the relays the boundary opens at launch are in place.
	controlReady = "ready"

	handoffSocket   = "socket"
	handoffNoSocket = "no_socket"
)

// socketRequestTimeout bounds the wait for the inner helper's answer.
const socketRequestTimeout = 5 * time.Second

// awaitSetup answers socket requests until the outer helper says its launch
// relays are in place, so the target starts with them.
func awaitSetup() error {
	for {
		ready, err := answerControl()
		if err != nil {
			return fmt.Errorf("supervisor setup: %w", err)
		}
		if ready {
			return nil
		}
	}
}

// serveSocketRequests answers socket requests until the outer helper closes
// the control socket.
func serveSocketRequests() {
	for {
		if _, err := answerControl(); err != nil {
			return
		}
	}
}

// answerControl handles one control message and reports whether it was ready.
func answerControl() (bool, error) {
	buf := make([]byte, maxHandoff)
	n, err := unix.Read(controlFD, buf)
	switch {
	case errors.Is(err, unix.EINTR):
		return false, nil
	case err != nil:
		return false, err
	case n == 0:
		return false, io.EOF
	}
	var m controlMessage
	if err := json.Unmarshal(buf[:n], &m); err != nil {
		// Only the outer helper writes here; anything else ends the sandbox.
		return false, fmt.Errorf("control message: %w", err)
	}
	switch m.Kind {
	case controlReady:
		return true, nil
	case controlSocket:
		return false, answerSocket(m.Family)
	}
	return false, fmt.Errorf("control message kind %q", m.Kind)
}

func answerSocket(family int) error {
	if family == unix.AF_INET || family == unix.AF_INET6 {
		if fd, err := unix.Socket(family, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0); err == nil {
			err = sendHandoff(handoff{Kind: handoffSocket}, fd)
			_ = unix.Close(fd)
			return err
		}
	}
	header, err := json.Marshal(handoff{Kind: handoffNoSocket})
	if err != nil {
		return err
	}
	return unix.Sendmsg(controlFD, header, nil, nil, 0)
}

// socketFactory is the outer helper's side of the socket requests.
type socketFactory struct {
	control *os.File
	mu      sync.Mutex
	replies chan int
	// broken is set once an answer went missing; a late one could otherwise
	// answer the next request.
	broken bool
}

func newSocketFactory(control *os.File) *socketFactory {
	return &socketFactory{control: control, replies: make(chan int, 1)}
}

// socket returns a datagram socket of family inside the sandbox's network
// namespace.
func (f *socketFactory) socket(family int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.broken {
		return -1, errors.New("sandbox socket requests stopped")
	}
	if err := f.send(controlMessage{Kind: controlSocket, Family: family}); err != nil {
		return -1, err
	}
	select {
	case fd := <-f.replies:
		if fd < 0 {
			return -1, errors.New("the sandbox could not create a socket")
		}
		return fd, nil
	case <-time.After(socketRequestTimeout):
		f.broken = true
		return -1, errors.New("no answer to a sandbox socket request")
	}
}

// ready tells the inner helper the launch relays are in place.
func (f *socketFactory) ready() {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = f.send(controlMessage{Kind: controlReady})
}

func (f *socketFactory) send(m controlMessage) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = unix.Write(int(f.control.Fd()), raw)
	return err
}

// deliver hands the inner helper's answer to the waiting request; -1 is a
// refusal. An answer nobody waits for is closed.
func (f *socketFactory) deliver(fd int) {
	select {
	case f.replies <- fd:
	default:
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}
}
