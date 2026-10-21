//go:build linux

package launchguard

import (
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// supervisor holds the generation's lifetime lock until every descendant of
// the launch has exited, and serves the broker. As the subreaper, every
// process the launch leaves behind is reparented here, so no descendant can
// outlive the lock, and one reaper collects both those and brokered programs.
type supervisor struct {
	mu    sync.Mutex
	calls map[int]*brokerCall
}

func supervise(lifetime, broker *os.File) int {
	defer func() { _ = lifetime.Close() }()
	s := &supervisor{calls: map[int]*brokerCall{}}
	go s.serveBroker(broker)
	status := 0
	for {
		var wait unix.WaitStatus
		pid, err := unix.Wait4(-1, &wait, 0, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ECHILD):
			return status
		case err != nil:
			return 1
		}
		if s.finished(pid, wait) {
			continue
		}
		switch {
		case wait.Exited():
			status = wait.ExitStatus()
		case wait.Signaled():
			status = 128 + int(wait.Signal())
		}
	}
}

// finished reports a brokered program's exit on its call channel.
func (s *supervisor) finished(pid int, wait unix.WaitStatus) bool {
	s.mu.Lock()
	call, ok := s.calls[pid]
	delete(s.calls, pid)
	s.mu.Unlock()
	if !ok {
		return false
	}
	event := BrokerEvent{Exited: true}
	switch {
	case wait.Exited():
		event.Code = wait.ExitStatus()
	case wait.Signaled():
		event.Signal = int(wait.Signal())
	}
	if call.abandoned {
		_ = unix.Close(call.channel)
		return true
	}
	sendBrokerEvent(call.channel, event)
	// The channel's watcher owns the descriptor: shutting it down ends that
	// watch, which then closes it, so its number is never reused under it.
	_ = unix.Shutdown(call.channel, unix.SHUT_RDWR)
	return true
}

// serveBroker answers requests until every protected holder of the socket
// has closed it.
func (s *supervisor) serveBroker(broker *os.File) {
	defer func() { _ = broker.Close() }()
	payload := make([]byte, maxBrokerRequest+1)
	control := make([]byte, unix.CmsgSpace(brokerRequestFDs*4)+brokerControlSize)
	for {
		n, oobn, flags, _, err := unix.Recvmsg(int(broker.Fd()), payload, control, unix.MSG_CMSG_CLOEXEC)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil || n == 0 && oobn == 0:
			return
		}
		if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n > maxBrokerRequest {
			closeRights(control[:oobn])
			continue
		}
		request, fds, err := decodeBrokerRequest(payload[:n], control[:oobn])
		if err != nil {
			continue
		}
		s.start(request, fds)
	}
}

func (s *supervisor) start(request BrokerRequest, fds []int) {
	channel := fds[0]
	defer func() {
		for _, fd := range fds[1:] {
			_ = unix.Close(fd)
		}
	}()
	s.mu.Lock()
	pid, err := startBrokered(request, fds)
	if err == nil {
		s.calls[pid] = &brokerCall{channel: channel, pid: pid}
	}
	s.mu.Unlock()
	if err != nil {
		sendBrokerEvent(channel, BrokerEvent{Error: err.Error()})
		_ = unix.Close(channel)
		return
	}
	sendBrokerEvent(channel, BrokerEvent{Started: pid})
	go s.endWhenAbandoned(channel, pid)
}

// endWhenAbandoned ends a brokered program's session when its caller closes
// the call channel before the program exited.
func (s *supervisor) endWhenAbandoned(channel, pid int) {
	buf := make([]byte, 1)
	for {
		n, err := unix.Read(channel, buf)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if n == 0 || err != nil {
			break
		}
	}
	s.mu.Lock()
	call, running := s.calls[pid]
	if running {
		// The program leads its own session and group; the reaper collects
		// it and then closes the abandoned channel.
		call.abandoned = true
		_ = unix.Kill(-pid, unix.SIGKILL)
	}
	s.mu.Unlock()
	if !running {
		_ = unix.Close(channel)
	}
}

func closeRights(control []byte) {
	messages, err := unix.ParseSocketControlMessage(control)
	if err != nil {
		return
	}
	for _, message := range messages {
		if fds, err := unix.ParseUnixRights(&message); err == nil {
			for _, fd := range fds {
				_ = unix.Close(fd)
			}
		}
	}
}
