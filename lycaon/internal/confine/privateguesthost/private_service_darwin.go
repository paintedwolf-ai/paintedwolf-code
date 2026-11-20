//go:build darwin

package privateguesthost

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"golang.org/x/sys/unix"
)

func launchService(_ context.Context) (net.Conn, error) {
	payload, err := configlayout.OpenPrivateGuestPayload()
	if err != nil {
		return nil, err
	}
	defer payload.Close()
	conn, child, err := activationChannel()
	if err != nil {
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

func activationDescriptors() ([2]int, error) {
	syscall.ForkLock.RLock()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err == nil {
		unix.CloseOnExec(pair[0])
		unix.CloseOnExec(pair[1])
	}
	syscall.ForkLock.RUnlock()
	return pair, err
}
