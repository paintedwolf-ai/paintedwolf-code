// Package linuxruntime activates the packaged service userland under the caller's resource limits.
package linuxruntime

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const hierarchy = "/sys/fs/cgroup"

func processGroup(pid string) (string, error) {
	raw, err := os.ReadFile(filepath.Join("/proc", pid, "cgroup"))
	if err != nil {
		return "", err
	}
	return unifiedGroup(string(raw))
}

func unifiedGroup(raw string) (string, error) {
	for _, line := range strings.Split(raw, "\n") {
		if path, ok := strings.CutPrefix(line, "0::"); ok && filepath.IsAbs(path) && filepath.Clean(path) == path {
			return filepath.Join(hierarchy, path), nil
		}
	}
	return "", errors.New("unified cgroup unavailable")
}

func limits() map[string]int64 { return map[string]int64{"memory.max": 512 << 20, "pids.max": 256} }

// Acquire selects the resource boundary before submitting any workload.
func Acquire(image string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	origin, err := processGroup("self")
	if err != nil {
		return err
	}
	leaf, err := delegatedLeaf(origin)
	if err != nil {
		return err
	}
	if leaf != "" {
		defer os.Remove(leaf)
		return startLeaf(image, leaf)
	}
	manager, err := nestedManager(origin)
	if err != nil {
		return err
	}
	if manager != "" {
		return managerScope(image, manager, origin)
	}
	return runVM(image)
}

func delegatedLeaf(origin string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(origin, "cgroup.subtree_control"))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var memory, pids bool
	for _, c := range strings.Fields(string(raw)) {
		memory = memory || c == "memory"
		pids = pids || c == "pids"
	}
	if !memory || !pids {
		return "", nil
	}
	leaf, err := os.MkdirTemp(origin, "pw-session-")
	if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EROFS) || errors.Is(err, unix.EPERM) {
		return "", nil
	}
	return leaf, err
}

// ControllerCommand prepares private infrastructure with a fixed root and empty environment.
func ControllerCommand(image string, args ...string) *exec.Cmd {
	cmd := exec.Command(image, args...)
	cmd.Dir = "/"
	cmd.Env = []string{}
	cmd.Stderr = os.Stderr
	return cmd
}

func startLeaf(image, leaf string) error {
	for file, limit := range limits() {
		if err := os.WriteFile(filepath.Join(leaf, file), []byte(strconv.FormatInt(limit, 10)), 0600); err != nil {
			return err
		}
	}
	group, err := os.Open(leaf)
	if err != nil {
		return err
	}
	defer group.Close()
	cmd := ControllerCommand(image, "--launch-native")
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(group.Fd()), Pdeathsig: syscall.SIGKILL}
	return cmd.Run()
}

func nestedManager(origin string) (string, error) {
	manager := "/usr/bin/systemd-run"
	if _, err := os.Stat(manager); errors.Is(err, os.ErrNotExist) {
		manager = "/run/current-system/sw/bin/systemd-run"
	}
	if _, err := os.Stat(manager); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	path := "/run/user/" + strconv.Itoa(os.Getuid()) + "/systemd/private"
	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ECONNREFUSED) {
			return "", nil
		}
		return "", err
	}
	defer conn.Close()
	raw, err := conn.(*net.UnixConn).SyscallConn()
	if err != nil {
		return "", err
	}
	var peer *unix.Ucred
	var credentials error
	if err := raw.Control(func(fd uintptr) { peer, credentials = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return "", err
	}
	if credentials != nil {
		return "", credentials
	}
	if peer == nil || peer.Uid != uint32(os.Getuid()) {
		return "", errors.New("unexpected resource manager peer")
	}
	group, err := processGroup(strconv.Itoa(int(peer.Pid)))
	if err != nil {
		return "", err
	}
	if !contained(origin, group) {
		return "", nil
	}
	return manager, nil
}

func contained(parent, path string) bool {
	return parent == path || strings.HasPrefix(path, parent+"/")
}

func managerScope(image, manager, origin string) error {
	file, err := trustedFile(manager, true)
	if err != nil {
		return err
	}
	defer file.Close()
	cmd := ControllerCommand("/proc/self/fd/3", "--user", "--scope", "--quiet", "--collect", "--property=TasksMax=256", "--property=MemoryMax=512M", "--", image, "--launch-native")
	cmd.Args[0] = manager
	cmd.ExtraFiles = []*os.File{file}
	cmd.Stdin = os.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.Env = []string{"XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(os.Getuid()), "PW_SERVICE_RESOURCE_PARENT=" + origin}
	return cmd.Run()
}

// Verify confirms that the kernel placed the controller inside its granted resource domain.
func Verify() error {
	group, err := processGroup("self")
	if err != nil {
		return err
	}
	var st unix.Statfs_t
	if err := unix.Statfs(group, &st); err != nil {
		return err
	}
	if st.Type != unix.CGROUP2_SUPER_MAGIC {
		return errors.New("private service requires a kernel resource domain")
	}
	members, err := os.Open(filepath.Join(group, "cgroup.procs"))
	if err != nil {
		return err
	}
	defer members.Close()
	scan := bufio.NewScanner(members)
	member := false
	for scan.Scan() {
		if scan.Text() == strconv.Itoa(os.Getpid()) {
			member = true
			break
		}
	}
	if err := scan.Err(); err != nil {
		return err
	}
	if !member {
		return errors.New("private service is outside its claimed resource domain")
	}
	if parent := os.Getenv("PW_SERVICE_RESOURCE_PARENT"); parent != "" && !contained(parent, group) {
		return errors.New("private service left its resource domain")
	}
	for file, max := range limits() {
		data, err := os.ReadFile(filepath.Join(group, file))
		if err != nil {
			return err
		}
		limit, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err != nil || limit <= 0 || limit > max {
			return fmt.Errorf("invalid private service %s limit", file)
		}
	}
	return nil
}
