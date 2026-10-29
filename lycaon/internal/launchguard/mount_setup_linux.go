//go:build linux

package launchguard

import (
	"errors"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// The startup transaction owns its children and socket until reconciliation.
// Its absolute timer interrupts I/O independently of EINTR and filesystem
// delays in a caller-credential child; it never targets shared processes.
type mountViewSetup struct {
	mu       sync.Mutex
	socket   int
	expired  bool
	children []mountSetupChild
	timer    *time.Timer
	finished chan struct{}
}

type mountSetupChild struct {
	command *exec.Cmd
	done    <-chan error
}

func newMountViewSetup(socket int) *mountViewSetup {
	setup := &mountViewSetup{socket: socket, finished: make(chan struct{})}
	setup.timer = time.AfterFunc(mountSourceSetupLimit, func() {
		defer close(setup.finished)
		setup.mu.Lock()
		defer setup.mu.Unlock()
		setup.expired = true
		_ = unix.Shutdown(setup.socket, unix.SHUT_RDWR)
		for _, child := range setup.children {
			_ = child.command.Process.Kill()
		}
	})
	return setup
}

func (setup *mountViewSetup) start(command *exec.Cmd) error {
	setup.mu.Lock()
	defer setup.mu.Unlock()
	if setup.expired {
		return errors.New("private module-view setup deadline exceeded")
	}
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	setup.children = append(setup.children, mountSetupChild{command: command, done: done})
	go func() { done <- command.Wait() }()
	return nil
}

// Stop completes before callers close socket descriptors, so a late timer
// cannot act on a descriptor number reused by another launch operation.
func (setup *mountViewSetup) stop() error {
	if setup.timer != nil {
		if !setup.timer.Stop() {
			<-setup.finished
		}
		setup.timer = nil
	}
	setup.mu.Lock()
	defer setup.mu.Unlock()
	if setup.expired {
		return errors.New("private module-view setup deadline exceeded")
	}
	return nil
}

func (setup *mountViewSetup) abort() {
	_ = setup.stop()
	for _, child := range setup.children {
		_ = child.command.Process.Kill()
	}
	deadline := time.After(time.Second)
	for _, child := range setup.children {
		select {
		case <-child.done:
		case <-deadline:
			// No readiness follows failure. The bootstrap subreaper retains
			// ownership of any child still completing kernel teardown.
			return
		}
	}
}
