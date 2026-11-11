//go:build linux

// diagnoexec replicates the protected launch's NOEXEC mount posture around
// the exact glycin bwrap command inside a throwaway user and mount
// namespace. The noexec mode makes the namespace's mounts private, sets
// MOUNT_ATTR_NOEXEC on "/" recursively, restores exec on the bwrap and
// loader files through the same file-typed projection mounts the protected
// launch builds, and then runs the exact bwrap command; the control mode
// skips the posture. The test grants ambient CAP_SYS_ADMIN for the setup
// and this helper drops every capability before spawning bwrap so the
// sandbox sees the same unprivileged identity as the failing launch.
// Temporary diagnostic for the upgrade-corpus launchguard failure; remove
// with the diag test.
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

const (
	bwrapPath  = "/usr/bin/bwrap"
	loaderPath = "/usr/libexec/glycin-loaders/2+/glycin-svg"
)

func main() {
	mode := "control"
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "-mode" && i+1 < len(args) {
			i++
			mode = args[i]
		}
	}
	// Contain every later mount change inside this namespace before
	// touching anything; the copied mounts keep shared propagation.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		fmt.Printf("DIAGNOEXEC mode=%s fatal=isolate err=%v\n", mode, err)
		os.Exit(1)
	}
	if mode == "noexec" {
		if err := unix.MountSetattr(unix.AT_FDCWD, "/", unix.AT_RECURSIVE, &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_NOEXEC}); err != nil {
			fmt.Printf("DIAGNOEXEC mode=%s fatal=noexec-root err=%v\n", mode, err)
			os.Exit(1)
		}
		for _, path := range []string{bwrapPath, loaderPath} {
			if err := projectExecutableFile(path); err != nil {
				fmt.Printf("DIAGNOEXEC mode=%s fatal=project path=%s err=%v\n", mode, path, err)
				os.Exit(1)
			}
		}
		code, stderr, err := runDirect(loaderPath)
		if err != nil {
			fmt.Printf("DIAGNOEXEC mode=%s outside-loader spawn-err=%v\n", mode, err)
		} else {
			fmt.Printf("DIAGNOEXEC mode=%s outside-loader-exit=%d stderr=%q\n", mode, code, stderr)
		}
	}
	if info, err := os.Stat(bwrapPath); err != nil {
		fmt.Printf("DIAGNOEXEC mode=%s bwrap-stat err=%v\n", mode, err)
	} else {
		fmt.Printf("DIAGNOEXEC mode=%s bwrap-mode=%s\n", mode, info.Mode())
	}
	if err := dropCapabilities(); err != nil {
		fmt.Printf("DIAGNOEXEC mode=%s fatal=drop-caps err=%v\n", mode, err)
		os.Exit(1)
	}
	verdict, code, detail := runBwrap()
	fmt.Printf("DIAGNOEXEC mode=%s verdict=%s code=%d detail=%q\n", mode, verdict, code, detail)
}

// projectExecutableFile replicates the protected launch's file-root
// projection: clone the file's mount, clear NOEXEC on the clone, and
// move-mount the file-typed clone over the path.
func projectExecutableFile(path string) error {
	open := func() (int, error) {
		return unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
	}
	file, err := open()
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = unix.Close(file) }()
	tree, err := unix.OpenTree(file, "", unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC|unix.AT_EMPTY_PATH)
	if err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	defer func() { _ = unix.Close(tree) }()
	if err := unix.MountSetattr(tree, "", unix.AT_EMPTY_PATH, &unix.MountAttr{Attr_clr: unix.MOUNT_ATTR_NOEXEC, Propagation: unix.MS_PRIVATE}); err != nil {
		return fmt.Errorf("clear noexec: %w", err)
	}
	target, err := open()
	if err != nil {
		return fmt.Errorf("open target: %w", err)
	}
	defer func() { _ = unix.Close(target) }()
	if err := unix.MoveMount(tree, "", target, "", unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	return nil
}

// dropCapabilities clears the ambient set and every capability so bwrap's
// copied mounts lock exactly as they do for the unprivileged desktop.
func dropCapabilities() error {
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return err
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	return unix.Capset(&header, &unix.CapUserData{})
}

// runDirect execs the loader outside bwrap; exit 2 (missing --dbus-fd)
// proves the projection made the file executable in the namespace itself.
func runDirect(loader string) (int, string, error) {
	command := exec.Command(loader)
	command.Env = []string{"HOME=/tmp-home", "XDG_RUNTIME_DIR=/tmp-run", "XDG_CACHE_HOME=/tmp-home"}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return -1, "", err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		return code, stderr.String(), nil
	case <-time.After(4 * time.Second):
		_ = command.Process.Kill()
		<-done
		return -1, stderr.String(), fmt.Errorf("loader stayed running")
	}
}

// runBwrap spawns the exact bwrap command glycin builds for the SVG loader,
// with the dbus socket at fd 3 and no seccomp filter, and reports whether
// the loader stays alive or exits.
func runBwrap() (string, int, string) {
	cache, err := os.MkdirTemp("", "diag-noexec-cache-")
	if err != nil {
		return "ERROR", -1, err.Error()
	}
	defer func() { _ = os.RemoveAll(cache) }()
	glycinCache := filepath.Join(cache, ".cache", "glycin", "usr", "libexec", "glycin-loaders", "2+", "glycin-svg")
	if err := os.MkdirAll(filepath.Join(glycinCache, "fontconfig"), 0o755); err != nil {
		return "ERROR", -1, err.Error()
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return "ERROR", -1, err.Error()
	}
	peer, child := os.NewFile(uintptr(fds[0]), "diag-noexec-peer"), os.NewFile(uintptr(fds[1]), "diag-noexec-child")
	defer func() { _ = peer.Close(); _ = child.Close() }()
	command := exec.Command(bwrapPath,
		"--unshare-all", "--die-with-parent", "--chdir", "/",
		"--ro-bind", "/usr", "/usr",
		"--dev", "/dev",
		"--ro-bind-try", "/etc/ld.so.cache", "/etc/ld.so.cache",
		"--ro-bind-try", "/nix/store", "/nix/store",
		"--tmpfs", "/tmp-home",
		"--tmpfs", "/tmp-run",
		"--clearenv",
		"--setenv", "HOME", "/tmp-home",
		"--setenv", "XDG_RUNTIME_DIR", "/tmp-run",
		"--symlink", "/usr/lib", "/lib",
		"--symlink", "/usr/lib32", "/lib32",
		"--symlink", "/usr/lib64", "/lib64",
		"--ro-bind-try", "/etc/fonts/conf.d", "/etc/fonts/conf.d",
		"--ro-bind-try", "/etc/fonts/fonts.conf", "/etc/fonts/fonts.conf",
		"--ro-bind-try", "/var/cache/fontconfig", "/var/cache/fontconfig",
		"--bind-try", glycinCache, glycinCache,
		"--setenv", "XDG_CACHE_HOME", glycinCache,
		loaderPath,
		"--dbus-fd", "3",
	)
	command.Env = nil
	command.ExtraFiles = []*os.File{child}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		return "SPAWN-ERROR", -1, err.Error()
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		return "EXITED", code, stderr.String()
	case <-time.After(8 * time.Second):
		_ = command.Process.Kill()
		<-done
		return "ALIVE", 0, stderr.String()
	}
}
