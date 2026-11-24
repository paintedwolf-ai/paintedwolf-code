// Package githelper binds per-invocation Unix helper sockets to a launched Git process.
package githelper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
	"github.com/lycaon/lycaon/internal/socketref"
)

// SSHSocketEnv carries the per-invocation managed SSH service socket to Git's helper.
const SSHSocketEnv = "LYCAON_GIT_SSH_SOCKET"

// SetupDeadline bounds waiting for process binding and reading a helper request.
const SetupDeadline = 10 * time.Second

// ErrUnbound reports that the invocation never bound a served process before deadline.
var ErrUnbound = errors.New("git helper service was never bound to its invocation")

// ErrClosed reports that the service closed before peer verification finished.
var ErrClosed = errors.New("git helper service is closed")

// ServedProcess identifies one started process by PID and start time.
type ServedProcess struct {
	PID   int
	Start int64
}

// Binding holds the immutable process identity one helper socket serves.
type Binding struct {
	helper    string
	gitBinary string
	served    ServedProcess
	bindErr   error
	bound     chan struct{}
	closed    chan struct{}
	bindOnce  sync.Once
	closeOnce sync.Once
}

// NewBinding prepares peer verification for helpers descending from gitBinary.
func NewBinding(gitBinary string) (*Binding, error) {
	if strings.TrimSpace(gitBinary) == "" {
		return nil, errors.New("git helper service requires the served git binary")
	}
	helper, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("git helper service: %w", err)
	}
	resolvedGit, err := filepath.EvalSymlinks(gitBinary)
	if err != nil {
		return nil, fmt.Errorf("git helper service: %w", err)
	}
	return &Binding{
		helper:    helper,
		gitBinary: resolvedGit,
		bound:     make(chan struct{}),
		closed:    make(chan struct{}),
	}, nil
}

// Bind records the started Git or supervisor process once and wakes waiting handlers.
func (b *Binding) Bind(pid int) error {
	ran := false
	b.bindOnce.Do(func() {
		ran = true
		select {
		case <-b.closed:
			b.bindErr = ErrClosed
		default:
			start, ok := osprocess.StartTime(pid)
			if !ok {
				b.bindErr = fmt.Errorf("git helper service: served process %d is not running", pid)
			} else {
				b.served = ServedProcess{PID: pid, Start: start}
			}
		}
		close(b.bound)
	})
	if !ran {
		return errors.New("git helper service is already bound to its invocation")
	}
	return b.bindErr
}

// BindError returns the error recorded when the first Bind failed, or nil.
func (b *Binding) BindError() error {
	select {
	case <-b.bound:
		if errors.Is(b.bindErr, ErrClosed) {
			return nil
		}
		return b.bindErr
	default:
		return nil
	}
}

// Close wakes any handler still waiting for Bind.
func (b *Binding) Close() {
	b.closeOnce.Do(func() { close(b.closed) })
}

// Verify waits for Bind within deadline, then verifies the Unix peer and returns
// any initial request bytes consumed with the peer credentials.
func (b *Binding) Verify(ctx context.Context, conn *net.UnixConn, deadline time.Time) ([]byte, error) {
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	wait := time.Until(deadline)
	if wait <= 0 {
		return nil, ErrUnbound
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-b.bound:
		if b.bindErr != nil {
			return nil, b.bindErr
		}
	case <-b.closed:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, ErrUnbound
	}
	return verifyPeer(conn, b.helper, b.gitBinary, b.served)
}

// Listen allocates a private service directory and opens socketName inside it.
func Listen(ctx context.Context, socketName string) (*net.UnixListener, string, error) {
	directory, err := socketref.ServiceDirectory()
	if err != nil {
		return nil, "", err
	}
	lc := net.ListenConfig{Control: enableListenerCredentials}
	ln, err := lc.Listen(ctx, "unix", filepath.Join(directory, socketName))
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, "", err
	}
	unixLn, ok := ln.(*net.UnixListener)
	if !ok {
		_ = ln.Close()
		_ = os.RemoveAll(directory)
		return nil, "", errors.New("unix listener type mismatch")
	}
	return unixLn, directory, nil
}

// Dial connects to an absolute helper socket path.
func Dial(socketPath string) (*net.UnixConn, error) {
	if socketPath == "" {
		return nil, errors.New("git helper socket is not set")
	}
	if !filepath.IsAbs(socketPath) {
		return nil, fmt.Errorf("git helper socket %q is not an absolute path", socketPath)
	}
	return net.DialUnix("unix", nil, &net.UnixAddr{Name: filepath.Clean(socketPath), Net: "unix"})
}
