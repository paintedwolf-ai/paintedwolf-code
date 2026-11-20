//go:build darwin

package privateguesthost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/serviceguest"
	"golang.org/x/sys/unix"
)

// OpenPrivateGuest starts an ephemeral VM and verifies its private agent channel.
func OpenPrivateGuest(ctx context.Context) (*serviceguest.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, err := configlayout.OpenPrivateGuestPayload()
	if err != nil {
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	transport, err := launchPrivateGuest(payload)
	payload.Close()
	if err != nil {
		return nil, errors.Join(serviceguest.ErrUnavailable, err)
	}
	client, err := serviceguest.Open(ctx, transport)
	if err != nil && ctx.Err() == nil {
		return nil, errors.Join(serviceguest.ErrUnavailable, err, transport.Close())
	}
	return client, err
}

func launchPrivateGuest(payload *configlayout.PrivateGuestPayload) (net.Conn, error) {
	syscall.ForkLock.RLock()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(pair[0])
		unix.CloseOnExec(pair[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "private guest host channel")
	child := os.NewFile(uintptr(pair[1]), "private guest activation")
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		_ = child.Close()
		return nil, err
	}
	process, err := spawnGuest(payload.Helper, payload.CDHash, append([]*os.File{child}, payload.Boot...))
	_ = child.Close()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("start private service helper: %w", err)
	}
	channel := &privateGuestChannel{Conn: conn, process: process, done: make(chan struct{})}
	go func() {
		state, err := process.Wait()
		channel.status = err
		if err == nil && !state.Success() {
			channel.status = fmt.Errorf("private service helper: %s", state)
		}
		close(channel.done)
	}()
	return channel, nil
}

type privateGuestChannel struct {
	net.Conn
	process *os.Process
	done    chan struct{}
	once    sync.Once
	status  error
}

func (c *privateGuestChannel) Close() error {
	c.once.Do(func() {
		_ = c.Conn.Close()
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
			_ = c.process.Kill()
			<-c.done
		}
	})
	return c.status
}
