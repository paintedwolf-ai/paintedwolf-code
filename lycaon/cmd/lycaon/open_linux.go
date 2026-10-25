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
	started := &desktopStart{cmd: cmd, grace: launch.grace, exited: make(chan error, 1), done: make(chan struct{})}
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
	cmd    *exec.Cmd
	grace  time.Duration
	exited chan error
	done   chan struct{}
	stop   sync.Once
}

func (d *desktopStart) wait() {
	err := d.cmd.Wait()
	if err == nil {
		err = errors.New("exit status 0")
	}
	d.exited <- err
	close(d.done)
}

func (d *desktopStart) Exited() <-chan error { return d.exited }

// Stop signals only the session this command created, then waits for its leader.
func (d *desktopStart) Stop() {
	d.stop.Do(func() {
		group := -d.cmd.Process.Pid
		_ = syscall.Kill(group, syscall.SIGTERM)
		select {
		case <-d.done:
		case <-time.After(d.grace):
			_ = syscall.Kill(group, syscall.SIGKILL)
			<-d.done
		}
	})
}
