//go:build linux

package launchguard

import (
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
)

// superviseLaunch holds the generation's lifetime lock until every
// descendant of the launch has exited. As the subreaper, every process the
// launch leaves behind, the broker agent's included, is reparented here, so
// no descendant can outlive the lock. It returns the last exit status.
func superviseLaunch(lifetime *os.File) int {
	defer func() { _ = lifetime.Close() }()
	status := 0
	for {
		var wait unix.WaitStatus
		_, err := unix.Wait4(-1, &wait, 0, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ECHILD):
			return status
		case err != nil:
			return 1
		}
		switch {
		case wait.Exited():
			status = wait.ExitStatus()
		case wait.Signaled():
			status = 128 + int(wait.Signal())
		}
	}
}

// brokerAgent starts brokered programs and is their parent: one reaper
// collects them and reports each exit on its call channel.
type brokerAgent struct {
	mu    sync.Mutex
	calls map[int]*brokerCall
}

// serve answers the broker until every protected holder of the socket has
// closed it and every program it started has exited.
func (s *brokerAgent) serve(broker *os.File) {
	done := make(chan struct{})
	go func() { s.serveBroker(broker); close(done) }()
	for {
		var wait unix.WaitStatus
		pid, err := unix.Wait4(-1, &wait, 0, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ECHILD):
			select {
			case <-done:
				return
			case <-time.After(100 * time.Millisecond):
				continue
			}
		case err != nil:
			return
		}
		s.finished(pid, wait)
	}
}

// finished reports a brokered program's exit on its call channel.
func (s *brokerAgent) finished(pid int, wait unix.WaitStatus) bool {
	s.mu.Lock()
	call, ok := s.calls[pid]
	delete(s.calls, pid)
	s.mu.Unlock()
	if !ok {
		return false
	}
	event := launchbroker.Event{Exited: true}
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
	launchbroker.SendEvent(call.channel, event)
	// The channel's watcher owns the descriptor: shutting it down ends that
	// watch, which then closes it, so its number is never reused under it.
	_ = unix.Shutdown(call.channel, unix.SHUT_RDWR)
	return true
}

// serveBroker answers requests until every protected holder of the socket
// has closed it.
func (s *brokerAgent) serveBroker(broker *os.File) {
	defer func() { _ = broker.Close() }()
	payload := make([]byte, launchbroker.MaxRequest+1)
	control := make([]byte, launchbroker.ControlSpace())
	for {
		n, oobn, flags, _, err := unix.Recvmsg(int(broker.Fd()), payload, control, unix.MSG_CMSG_CLOEXEC)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case err != nil || n == 0 && oobn == 0:
			return
		}
		if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 || n > launchbroker.MaxRequest {
			for _, fd := range launchbroker.Rights(control[:oobn]) {
				_ = unix.Close(fd)
			}
			continue
		}
		request, fds, err := launchbroker.Decode(payload[:n], control[:oobn])
		if err != nil {
			continue
		}
		s.start(request, fds)
	}
}

func (s *brokerAgent) start(request launchbroker.Request, fds []int) {
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
		launchbroker.SendEvent(channel, launchbroker.Event{Started: pid})
	}
	s.mu.Unlock()
	if err != nil {
		launchbroker.SendEvent(channel, launchbroker.Event{Error: err.Error()})
		_ = unix.Close(channel)
		return
	}
	go s.endWhenAbandoned(channel, pid)
}

// endWhenAbandoned ends a brokered program's session when its caller closes
// the call channel before the program exited.
func (s *brokerAgent) endWhenAbandoned(channel, pid int) {
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
