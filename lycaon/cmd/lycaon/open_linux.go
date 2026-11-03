package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/osprocess"
)

// linuxDesktopLauncher is the installed desktop entry point, the same program the
// desktop file runs. Nothing here searches PATH or accepts a caller-chosen program.
const linuxDesktopLauncher = "/usr/libexec/painted-wolf-code/pw-launch"

// stopGrace bounds how long a cancelled or timed-out start may take to exit before
// its process group is killed.
const stopGrace = 5 * time.Second

func startApp(context.Context) (appStart, error) {
	return startDesktop(desktopLaunch{path: linuxDesktopLauncher, anchor: "/", owner: 0,
		environment: os.Environ(), grace: stopGrace})
}

// desktopLaunch fixes which program starts and which owner it must trust. Ancestors
// from the program up to anchor must be owned by root or owner and not writable by
// anyone else; the installed launch anchors at "/" and trusts only root.
type desktopLaunch struct {
	path        string
	anchor      string
	owner       uint32
	environment []string
	grace       time.Duration
}

func startDesktop(launch desktopLaunch) (appStart, error) {
	if !graphicalSession(launch.environment) {
		return nil, errors.New("the Painted Wolf Code app is not running and this terminal has no graphical session; start it from your desktop")
	}
	if err := trustedLauncher(launch.path, launch.anchor, launch.owner); err != nil {
		return nil, err
	}
	cmd := exec.Command(launch.path) //nolint:gosec,noctx // G204 — fixed, ownership-verified installed launcher; the app outlives this CLI command
	cmd.Env = launch.environment
	cmd.Dir = "/"
	// Its own session: the app outlives this command and the terminal's signals.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start Painted Wolf Code: %w", err)
	}
	leader, _ := osprocess.IncarnationOf(cmd.Process.Pid)
	started := &desktopStart{cmd: cmd, leader: leader, grace: launch.grace, exited: make(chan error, 1), done: make(chan struct{}),
		observed: make(chan struct{}), reapAllowed: make(chan struct{}), signalGroup: osprocess.SignalGroup}
	go started.wait()
	return started, nil
}

func graphicalSession(environment []string) bool {
	for _, entry := range environment {
		for _, name := range []string{"WAYLAND_DISPLAY=", "DISPLAY="} {
			if strings.HasPrefix(entry, name) && len(entry) > len(name) {
				return true
			}
		}
	}
	return false
}

func trustedLauncher(path, anchor string, owner uint32) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("desktop launcher %q is not a clean absolute path", path)
	}
	if relative, err := filepath.Rel(anchor, path); err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return fmt.Errorf("desktop launcher %q is outside its trusted root", path)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("the Painted Wolf Code app is not running and is not installed on this system; start it from where it is installed")
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("desktop launcher %s is not an executable file", path)
	}
	for current := path; ; current = filepath.Dir(current) {
		if current != path {
			if info, err = os.Lstat(current); err != nil {
				return err
			}
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 && stat.Uid != owner || info.Mode().Perm()&0o022 != 0 || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("desktop launcher path %s is not controlled by its installer", current)
		}
		if current == anchor {
			return nil
		}
	}
}

// desktopStart owns one launched process group until the app is reachable.
type desktopStart struct {
	cmd                   *exec.Cmd
	leader                osprocess.Incarnation
	grace                 time.Duration
	exited                chan error
	done                  chan struct{}
	stop                  sync.Once
	mu                    sync.Mutex
	stopping, reaped      bool
	observed, reapAllowed chan struct{}
	signalGroup           func(osprocess.Incarnation, syscall.Signal) error
}

func (d *desktopStart) wait() {
	// Observe exit while leaving the leader unreaped. Its PID also pins the
	// session's group ID until Stop has finished any group-wide signaling.
	var info unix.Siginfo
	var observedErr error
	for {
		observedErr = unix.Waitid(unix.P_PID, d.cmd.Process.Pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(observedErr, unix.EINTR) {
			break
		}
	}
	d.mu.Lock()
	close(d.observed)
	if d.stopping {
		d.mu.Unlock()
		<-d.reapAllowed
		d.mu.Lock()
	}
	if observedErr != nil {
		// A failed observer cannot leave Wait holding the ownership lock on a
		// still-running child. Process.Kill uses the owned process handle.
		_ = d.cmd.Process.Kill()
	}
	err := d.cmd.Wait()
	if observedErr != nil {
		err = fmt.Errorf("observe desktop exit without releasing its identity: %w", observedErr)
	}
	d.reaped = true
	d.mu.Unlock()
	if err == nil {
		err = errors.New("exit status 0")
	}
	d.exited <- err
	close(d.done)
}

func (d *desktopStart) Exited() <-chan error { return d.exited }

// Stop retains the unreaped leader until its final group signal has completed.
func (d *desktopStart) Stop() {
	d.stop.Do(func() {
		d.mu.Lock()
		if d.reaped {
			d.mu.Unlock()
			<-d.done
			return
		}
		d.stopping = true
		_ = d.signalGroup(d.leader, syscall.SIGTERM)
		d.mu.Unlock()
		select {
		case <-d.observed:
		case <-time.After(d.grace):
		}
		// The leader has not been reaped, even if TERM made it exit. Finish
		// owned descendants before permitting Wait to release its group ID.
		d.mu.Lock()
		_ = d.signalGroup(d.leader, syscall.SIGKILL)
		close(d.reapAllowed)
		d.mu.Unlock()
		<-d.done
	})
}
