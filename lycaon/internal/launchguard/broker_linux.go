//go:build linux

package launchguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/launchbroker"
)

// The broker starts programs that must not run inside the protected
// namespace: unconfined engine launches, terminals and desktop handlers. It
// is the broker agent, the caller in the initial user namespace sharing the
// launch's mount namespace, so whatever it starts has exactly the authority
// any program the caller runs has, sees the running generation at the logical
// installation root, and has no authority over the protected processes. Only
// protected processes hold its socket. The protocol is launchbroker's.

// maxConcurrentBrokerCalls bounds the simultaneous active calls one launch's
// broker agent accepts, preventing a runaway caller from exhausting file
// descriptors or process slots.
const maxConcurrentBrokerCalls = 128

// brokerCall is one started program the broker agent's reaper reports on.
type brokerCall struct {
	channel int
	pid     int
	detach  bool
	// abandoned marks a call whose caller closed the channel first.
	abandoned bool
}

// startBrokered forks the requested program with the request's streams and
// extra descriptors.
func startBrokered(request launchbroker.Request, fds []int) (int, error) {
	path := request.Argv[0]
	switch {
	case !strings.Contains(path, "/"):
		resolved, err := lookPath(path, request.Environment)
		if err != nil {
			return 0, err
		}
		path = resolved
	case !filepath.IsAbs(path) || filepath.Clean(path) != path:
		return 0, errors.New("broker program path must be absolute and clean")
	}
	files := make([]uintptr, 0, len(fds)-1)
	for _, fd := range fds[1:] {
		files = append(files, uintptr(fd))
	}
	attributes := &syscall.ProcAttr{
		Dir:   request.Dir,
		Env:   request.Environment,
		Files: files,
		Sys:   &syscall.SysProcAttr{Setsid: true, Setctty: request.Terminal, Ctty: 0},
	}
	return syscall.ForkExec(path, request.Argv, attributes)
}

// lookPath resolves a program name through the request's own PATH.
func lookPath(name string, environment []string) (string, error) {
	path := ""
	for _, entry := range environment {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			path = value
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", errors.New(name + ": not found on the request's PATH")
}
