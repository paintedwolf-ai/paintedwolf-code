//go:build linux

package linuxsandbox

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Inside reports whether this process is the helper the outer helper
// relaunched: the first process of the fresh PID namespace, whose user
// namespace also differs from the engine's. The outer helper is never PID 1,
// and its own user namespace need not be the engine's: under the protected
// launch the broker starts it outside the engine's namespace.
func Inside(plan Plan) bool {
	host := plan.Attest.HostNamespaces[NamespaceUser]
	own, err := os.Readlink("/proc/self/ns/" + NamespaceUser)
	return os.Getpid() == 1 && err == nil && host != "" && own != host
}

// mountProc gives the new PID namespace its own process table first, so any
// later reader of /proc, including a failure path, sees sandbox processes only.
func mountProc() error {
	if err := unix.Mount("proc", "/proc", "proc", unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC, ""); err != nil {
		return classifyPrivilegedStep("mount /proc", err, appArmorLabel())
	}
	return nil
}

// makeRootPrivate keeps the sandbox's mounts from propagating anywhere.
func makeRootPrivate() error {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return &Unavailability{Code: UnavailableMount, Detail: "make / private: " + err.Error()}
	}
	return nil
}

// loopbackUp brings the namespace's loopback interface up; nothing else exists.
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("loopback socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("read lo flags: %w", err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("bring lo up: %w", err)
	}
	return nil
}

// namespacesDiffer confirms every namespace kind differs from the host's.
func namespacesDiffer(host map[string]string) error {
	var same []string
	for _, kind := range NamespaceKinds() {
		own, err := os.Readlink("/proc/self/ns/" + kind)
		if err != nil {
			return &Unavailability{Code: UnavailableNamespaces, Detail: kind + ": " + err.Error()}
		}
		if want, ok := host[kind]; !ok || own == want {
			same = append(same, kind)
		}
	}
	if len(same) > 0 {
		return &Unavailability{Code: UnavailableNamespaces, Detail: "shared with the host: " + strings.Join(same, ", ")}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
