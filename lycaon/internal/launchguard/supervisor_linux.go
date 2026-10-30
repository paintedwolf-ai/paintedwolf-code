//go:build linux

package launchguard

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/launchbroker"
)

// brokerAgent starts brokered programs and is their parent: one reaper
// collects them and reports each exit on its call channel.
type brokerAgent struct {
	mu      sync.Mutex
	closing bool
	calls   map[int]*brokerCall
}

// serve answers the broker until every protected holder of the socket has
// closed it and every non-detached program it started has exited.
func (s *brokerAgent) serve(broker *os.File) {
	sigchld := make(chan os.Signal, 1)
	signal.Notify(sigchld, unix.SIGCHLD)
	defer signal.Stop(sigchld)
	done := make(chan struct{})
	go func() {
		s.serveBroker(broker)
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		close(done)
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		reaped, noChildren := s.reapAvailable()
		if s.shouldExit(noChildren) {
			return
		}
		if reaped {
			continue
		}
		select {
		case <-sigchld:
		case <-done:
			_, noChildren := s.reapAvailable()
			if s.shouldExit(noChildren) {
				return
			}
		case <-ticker.C:
		}
	}
}

func (s *brokerAgent) reapAvailable() (bool, bool) {
	reaped := false
	for {
		var wait unix.WaitStatus
		pid, err := unix.Wait4(-1, &wait, unix.WNOHANG, nil)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case pid > 0:
			reaped = true
			s.finished(pid, wait)
		case errors.Is(err, unix.ECHILD):
			return reaped, true
		default:
			return reaped, false
		}
	}
}

func (s *brokerAgent) shouldExit(noChildren bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closing {
		return false
	}
	if noChildren {
		return true
	}
	for _, call := range s.calls {
		if !call.detach {
			return false
		}
	}
	return true
}

// finished reports a brokered program's exit on its call channel.
func (s *brokerAgent) finished(pid int, wait unix.WaitStatus) bool {
	s.mu.Lock()
	call, ok := s.calls[pid]
	delete(s.calls, pid)
	if !ok {
		s.mu.Unlock()
		return false
	}
	event := launchbroker.Event{Exited: true}
	switch {
	case wait.Exited():
		event.Code = wait.ExitStatus()
	case wait.Signaled():
		event.Signal = int(wait.Signal())
	}
	abandoned := call.abandoned
	channel := call.channel
	if !abandoned {
		launchbroker.SendEvent(channel, event)
		// The channel's watcher owns the descriptor: shutting it down ends that
		// watch, which then closes it, so its number is never reused under it.
		_ = unix.Shutdown(channel, unix.SHUT_RDWR)
	}
	s.mu.Unlock()
	if abandoned {
		_ = unix.Close(channel)
	}
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
	if s.closing {
		s.mu.Unlock()
		launchbroker.SendEvent(channel, launchbroker.Event{Error: "broker is closing"})
		_ = unix.Close(channel)
		return
	}
	if len(s.calls) >= maxConcurrentBrokerCalls {
		s.mu.Unlock()
		launchbroker.SendEvent(channel, launchbroker.Event{Error: "broker active call limit reached"})
		_ = unix.Close(channel)
		return
	}
	pid, err := startBrokered(request, fds)
	if err == nil {
		s.calls[pid] = &brokerCall{channel: channel, pid: pid, detach: request.Detach}
		// Send Started under s.mu before finished can observe s.calls[pid] and
		// emit Exited, guaranteeing Started -> Exited ordering on the channel.
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

// endWhenAbandoned ends a non-detached brokered program's session when its
// caller closes the call channel before the program exited.
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
		if call.detach {
			delete(s.calls, pid)
			running = false
		} else {
			// The program leads its own session and group; the reaper collects
			// it and then closes the abandoned channel.
			call.abandoned = true
			_ = unix.Kill(-pid, unix.SIGKILL)
		}
	}
	s.mu.Unlock()
	if !running {
		_ = unix.Close(channel)
	}
}
