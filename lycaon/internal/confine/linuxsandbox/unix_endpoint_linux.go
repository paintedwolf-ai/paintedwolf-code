//go:build linux

package linuxsandbox

import (
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// resolveSocketPath resolves the target's working directory and symbolic links
// before socket identity and grants are examined.
func resolveSocketPath(path string, target *targetProcess) string {
	if !filepath.IsAbs(path) {
		cwd, err := os.Readlink("/proc/" + strconv.Itoa(target.pid) + "/cwd")
		if err != nil {
			return ""
		}
		path = filepath.Join(cwd, path)
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

type delegatedServiceEndpoint struct {
	service DelegatedService
	path    string
}

// These are OS service locations, independent of the executed program or protocol text.
var delegatedSystemEndpoints = []delegatedServiceEndpoint{{DelegatedNixDaemon, "/nix/var/nix/daemon-socket/socket"}}

type pinnedUnixEndpoint struct {
	fd        int
	delegated DelegatedService
	openErr   error
}

func pinUnixEndpoint(path string, services []delegatedServiceEndpoint) pinnedUnixEndpoint {
	endpoint := pinnedUnixEndpoint{fd: -1}
	endpoint.fd, endpoint.openErr = unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	var target unix.Stat_t
	if endpoint.openErr == nil {
		endpoint.openErr = unix.Fstat(endpoint.fd, &target)
		if endpoint.openErr == nil && target.Mode&unix.S_IFMT != unix.S_IFSOCK {
			endpoint.openErr = unix.ENOTSOCK
		}
	}
	for _, service := range services {
		if filepath.Clean(path) == service.path {
			endpoint.delegated = service.service
			break
		}
		var observed unix.Stat_t
		if endpoint.openErr == nil && unix.Stat(service.path, &observed) == nil && observed.Mode&unix.S_IFMT == unix.S_IFSOCK && observed.Dev == target.Dev && observed.Ino == target.Ino {
			endpoint.delegated = service.service
			break
		}
	}
	return endpoint
}

func (endpoint pinnedUnixEndpoint) close() {
	if endpoint.fd >= 0 {
		_ = unix.Close(endpoint.fd)
	}
}

func (endpoint pinnedUnixEndpoint) connect(socket int) error {
	if endpoint.openErr != nil {
		return endpoint.openErr
	}
	// The held inode remains the checked recipient if its pathname is replaced.
	return unix.Connect(socket, &unix.SockaddrUnix{Name: "/proc/self/fd/" + strconv.Itoa(endpoint.fd)})
}
