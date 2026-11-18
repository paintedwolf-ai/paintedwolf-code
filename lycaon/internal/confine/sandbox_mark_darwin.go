//go:build darwin

package confine

/*
#include <stdlib.h>
#include <sys/types.h>

extern int sandbox_check(pid_t pid, const char *operation, int type, ...);
extern const int SANDBOX_CHECK_NO_REPORT;

// SANDBOX_FILTER_PATH is 1 in the sandbox filter enumeration.
static int confine_write_allowed(pid_t pid, const char *path) {
    return sandbox_check(pid, "file-write-data", 1 | SANDBOX_CHECK_NO_REPORT, path) == 0;
}
static int confine_pid_sandboxed(pid_t pid) {
    return sandbox_check(pid, 0, 0) == 1;
}
*/
import "C"

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"unsafe"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

// A sandbox outlives fork, exec, setsid and closed descriptors, which is how a
// daemon escapes every other attribution on macOS. Every profile this engine
// renders lets its processes write one marker file and not its sibling; no
// other profile does both, because both live in the control-plane tree that
// profiles deny unless they name it.
const (
	markOwnName     = "own"
	markForeignName = "foreign"
)

var engineMark struct {
	mu  sync.Mutex
	dir string
}

// EnableSandboxMark creates a fresh mark that every later profile carries and
// returns its directory, which the caller hands to whatever kills processes
// still running under it and then removes it.
func EnableSandboxMark() (string, error) {
	dir, err := createSandboxMark()
	if err != nil {
		return "", fmt.Errorf("sandbox ownership mark: %w", err)
	}
	engineMark.mu.Lock()
	engineMark.dir = dir
	engineMark.mu.Unlock()
	return dir, nil
}

// sandboxMarkRule admits a profile to the current mark. It must be the
// profile's last rule so no later deny overrides it.
func sandboxMarkRule() string {
	engineMark.mu.Lock()
	defer engineMark.mu.Unlock()
	if engineMark.dir == "" {
		return ""
	}
	return "(allow file-write-data (literal " + sbplString(filepath.Join(engineMark.dir, markOwnName)) + "))\n"
}

func createSandboxMark() (string, error) {
	root, err := configdir.Root()
	if err != nil {
		return "", err
	}
	parent := filepath.Join(root, "sandbox-marks")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	dir := filepath.Join(parent, strconv.Itoa(os.Getpid())+"-"+hex.EncodeToString(nonce))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	for _, name := range []string{markOwnName, markForeignName} {
		// Read-only, so the rule admits no actual write.
		file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400) // #nosec G304 -- a fixed name in a directory this process just created
		if err != nil {
			return "", err
		}
		_ = file.Close()
	}
	// Sandbox literals match the resolved path.
	return filepath.EvalSymlinks(dir)
}

// MarkedProcesses lists the live processes running under a profile that holds
// the markers in dir. Each incarnation is taken before its check, so a pid
// reused after the check never matches it.
func MarkedProcesses(dir string) []osprocess.Incarnation {
	own := C.CString(filepath.Join(dir, markOwnName))
	defer C.free(unsafe.Pointer(own))
	foreign := C.CString(filepath.Join(dir, markForeignName))
	defer C.free(unsafe.Pointer(foreign))
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var out []osprocess.Incarnation
	for i := range procs {
		pid := int(procs[i].Proc.P_pid)
		if pid <= 1 || pid == self {
			continue
		}
		inc, ok := osprocess.IncarnationOf(pid)
		if !ok {
			continue
		}
		cpid := C.pid_t(pid)
		if C.confine_pid_sandboxed(cpid) == 1 && C.confine_write_allowed(cpid, own) == 1 && C.confine_write_allowed(cpid, foreign) == 0 {
			out = append(out, inc)
		}
	}
	return out
}

// RemoveSandboxMark deletes the markers once nothing can run under them.
func RemoveSandboxMark(dir string) {
	if !filepath.IsAbs(dir) {
		return
	}
	_ = os.Remove(filepath.Join(dir, markOwnName))
	_ = os.Remove(filepath.Join(dir, markForeignName))
	_ = os.Remove(dir)
	// The parent goes only once no other engine's mark remains in it.
	_ = os.Remove(filepath.Dir(dir))
}
