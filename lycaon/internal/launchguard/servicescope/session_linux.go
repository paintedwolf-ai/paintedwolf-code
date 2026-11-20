package servicescope

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Acquire runs the fixed controller in one private leaf and reclaims it on exit.
func Acquire(image string) error {
	path, err := current()
	if err != nil {
		return err
	}
	suffix := filepath.Join(class, identity(os.Getuid()), "ordinary")
	if !strings.HasSuffix(path, "/"+suffix) {
		return managerScope(image)
	}
	pool := filepath.Join(filepath.Dir(path), "sessions")
	leaf, err := os.MkdirTemp(pool, "session-")
	if err != nil {
		return err
	}
	defer os.Remove(leaf)
	if err := setLimits(leaf, 1); err != nil {
		return err
	}
	group, err := os.Open(leaf)
	if err != nil {
		return err
	}
	defer group.Close()
	cmd := exec.Command(image, "--launch-native")
	cmd.Dir = "/"
	cmd.Env = []string{}
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(group.Fd()), Pdeathsig: syscall.SIGKILL}
	return cmd.Run()
}

func managerScope(image string) error {
	manager := "/usr/bin/systemd-run"
	if _, err := os.Stat(manager); errors.Is(err, os.ErrNotExist) {
		manager = "/run/current-system/sw/bin/systemd-run"
	}
	// A transient scope preserves the launching process's selected installation mount.
	cmd := exec.Command(manager, "--user", "--scope", "--quiet", "--collect", "--property=TasksMax=256", "--property=MemoryMax=512M", "--", image, "--launch-native")
	cmd.Stdin, cmd.Stderr = os.Stdin, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	cmd.Env = []string{"XDG_RUNTIME_DIR=/run/user/" + strconv.Itoa(os.Getuid())}
	cmd.Dir = "/"
	return cmd.Run()
}

// Verify reads the enforced ceilings from the actual process's leaf.
func Verify() error {
	group, err := current()
	if err != nil {
		return err
	}
	for file, max := range map[string]int64{"memory.max": 512 << 20, "pids.max": 256} {
		data, err := os.ReadFile(filepath.Join(group, file))
		if err != nil {
			return err
		}
		fields := strings.Fields(string(data))
		if len(fields) == 0 {
			return errors.New("missing private service limit")
		}
		limit, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || limit <= 0 || limit > max {
			return fmt.Errorf("invalid private service %s limit", file)
		}
	}
	return nil
}
