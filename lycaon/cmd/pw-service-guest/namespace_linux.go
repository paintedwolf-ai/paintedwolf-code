package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/serviceguest"
	"golang.org/x/sys/unix"
)

func runNative(mode string) error {
	if _, err := unix.Getpeername(0); err != nil {
		return fmt.Errorf("private activation: %w", err)
	}
	if mode == "--launch-native" {
		return launchNamespace()
	}
	if os.Getpid() != 1 || os.Geteuid() != 0 {
		return errors.New("invalid private namespace activation")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := isolateRoot(); err != nil {
		return fmt.Errorf("mount private environment: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	os.Clearenv()
	scratch, err := prepareWork(0)
	if err != nil {
		return err
	}
	return serviceguest.Serve(context.Background(), os.Stdin, newJobs(configuration{scratch: scratch, helper: "/init"}))
}

func launchNamespace() error {
	if err := verifyQuota(); err != nil {
		return err
	}
	image, err := os.Executable()
	if err != nil {
		return err
	}
	root, err := os.Open(filepath.Dir(image))
	if err != nil {
		return err
	}
	defer root.Close()
	cmd := exec.Command("/proc/self/exe", "--enter-native")
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	cmd.Dir = "/"
	cmd.Env = []string{}
	cmd.ExtraFiles = []*os.File{root}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}}, GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}}, Pdeathsig: syscall.SIGKILL,
	}
	return cmd.Run()
}

func verifyQuota() error {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	group, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "0::/")
	if !ok || group == "" || !filepath.IsLocal(group) {
		return errors.New("private service requires a bounded cgroup")
	}
	for file, max := range map[string]int64{"memory.max": 512 << 20, "pids.max": 256, "cpu.max": 1} {
		data, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", group, file))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(data))
		if len(fields) == 0 {
			return errors.New("missing private service limit")
		}
		limit, err := strconv.ParseInt(fields[0], 10, 64)
		if file == "cpu.max" && len(fields) == 2 {
			max, _ = strconv.ParseInt(fields[1], 10, 64)
		}
		if err != nil || limit <= 0 || limit > max {
			return fmt.Errorf("invalid private service %s limit", file)
		}
	}
	return nil
}

func isolateRoot() error {
	const root = "/tmp"
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return err
	}
	if err := unix.Mount("/proc/self/fd/3", root, "", unix.MS_BIND, ""); err != nil {
		return err
	}
	if err := unix.Close(3); err != nil {
		return err
	}
	if err := unix.Mount("", root, "", unix.MS_BIND|unix.MS_REMOUNT|unix.MS_RDONLY|unix.MS_NOSUID|unix.MS_NODEV, ""); err != nil {
		return err
	}
	for _, m := range []struct {
		path, kind, options string
		flags               uintptr
	}{
		{"dev", "tmpfs", "size=1M,mode=0755", unix.MS_NOSUID},
		{"proc", "proc", "", unix.MS_NOSUID | unix.MS_NOEXEC | unix.MS_NODEV},
		{"tmp", "tmpfs", "size=16M,mode=1777", unix.MS_NOSUID | unix.MS_NODEV},
		{"home/service", "tmpfs", "size=256M,mode=0700", unix.MS_NOSUID | unix.MS_NODEV},
	} {
		if err := unix.Mount(m.kind, filepath.Join(root, m.path), m.kind, m.flags, m.options); err != nil {
			return fmt.Errorf("mount %s: %w", m.path, err)
		}
	}
	dev := filepath.Join(root, "dev")
	for _, name := range []string{"null", "zero", "full", "tty", "random", "urandom"} {
		dest := filepath.Join(dev, name)
		if err := os.WriteFile(dest, nil, 0600); err != nil {
			return err
		}
		if err := unix.Mount(filepath.Join("/dev", name), dest, "", unix.MS_BIND, ""); err != nil {
			return err
		}
	}
	if err := os.Mkdir(filepath.Join(dev, "shm"), 0755); err != nil {
		return err
	}
	if err := unix.Mount("tmpfs", filepath.Join(dev, "shm"), "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "size=32M,mode=1777"); err != nil {
		return err
	}
	if err := os.Symlink("/proc/self/fd", filepath.Join(dev, "fd")); err != nil {
		return err
	}
	if err := unix.Chdir(root); err != nil {
		return err
	}
	if err := unix.PivotRoot(".", "."); err != nil {
		return err
	}
	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return err
	}
	return unix.Chdir("/")
}
