//go:build darwin

package sandboxmark

/*
#include <stdlib.h>
#include <sys/types.h>

extern int sandbox_check(pid_t pid, const char *operation, int type, ...);
extern const int SANDBOX_CHECK_NO_REPORT;

// SANDBOX_FILTER_PATH is 1 in the sandbox filter enumeration.
static int mark_write_allowed(pid_t pid, const char *path) {
    return sandbox_check(pid, "file-write-data", 1 | SANDBOX_CHECK_NO_REPORT, path) == 0;
}
static int mark_pid_sandboxed(pid_t pid) {
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
	"unsafe"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

// Enable creates a fresh mark that every later profile carries and returns its
// directory, which the caller hands to whatever kills processes still running
// under it and then removes it.
func Enable() (string, error) {
	dir, err := create()
	if err != nil {
		return "", fmt.Errorf("sandbox ownership mark: %w", err)
	}
	current.mu.Lock()
	current.dir = dir
	current.mu.Unlock()
	return dir, nil
}

func create() (string, error) {
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
	for _, name := range []string{ownName, foreignName} {
		// Read-only, so the rule admits no actual write.
		file, err := fseffect.OpenWrite(fseffect.Location{Root: dir, Rel: name}, false, 0o400)
		if err != nil {
			return "", err
		}
		_ = file.Close()
	}
	// Sandbox literals match the resolved path.
	return filepath.EvalSymlinks(dir)
}

// Processes lists the live processes running under a profile that holds the
// markers in dir. Each incarnation is taken before its check, so a pid reused
// after the check never matches it.
func Processes(dir string) []osprocess.Incarnation {
	own := C.CString(filepath.Join(dir, ownName))
	defer C.free(unsafe.Pointer(own))
	foreign := C.CString(filepath.Join(dir, foreignName))
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
		if C.mark_pid_sandboxed(cpid) == 1 && C.mark_write_allowed(cpid, own) == 1 && C.mark_write_allowed(cpid, foreign) == 0 {
			out = append(out, inc)
		}
	}
	return out
}
