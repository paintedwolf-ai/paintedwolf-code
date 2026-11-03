//go:build darwin

package confine

/*
#include <stdint.h>
#include <stdlib.h>
#include <unistd.h>
#include <sys/types.h>

// Direct declarations keep compiler attributes out of the binding.
extern int  sandbox_init(const char *profile, uint64_t flags, char **errorbuf);
extern void sandbox_free_error(char *errorbuf);

// A null operation reports current sandbox membership.
extern int sandbox_check(pid_t pid, const char *operation, int type, ...);

static int confine_is_sandboxed(void) {
    return sandbox_check(getpid(), 0, 0);
}
*/
import "C"

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"
)

// Available reports whether per-command confinement is enforced.
func Available() bool { return true }

// The profile applies to the current process.
func applySeatbelt(profile string) error {
	cProfile := C.CString(profile)
	defer C.free(unsafe.Pointer(cProfile))

	var errBuf *C.char
	//nolint:gocritic // dupSubExpr matches the cgo call expansion, not this source
	if rc := C.sandbox_init(cProfile, 0, &errBuf); rc != 0 {
		msg := "sandbox_init failed"
		if errBuf != nil {
			msg = C.GoString(errBuf)
			C.sandbox_free_error(errBuf)
		}
		return fmt.Errorf("seatbelt: %s", msg)
	}
	return nil
}

// processSandboxed reads current kernel membership.
func processSandboxed() (member bool, ok bool) {
	rc := C.confine_is_sandboxed()
	if rc < 0 {
		return false, false
	}
	return rc > 0, true
}

// attestConfined requires positive kernel membership.
func attestConfined() error {
	member, ok := processSandboxed()
	if !ok || !member {
		return fmt.Errorf("seatbelt: could not confirm this process is sandboxed " +
			"(sandbox_check) — refusing to run unconfined")
	}
	return nil
}

var darwinUserRootsOnce = sync.OnceValue(func() []string {
	var out []string
	for _, name := range []C.int{C._CS_DARWIN_USER_TEMP_DIR, C._CS_DARWIN_USER_CACHE_DIR} {
		n := C.confstr(name, nil, 0)
		if n == 0 {
			continue
		}
		buf := make([]byte, int(n))
		if C.confstr(name, (*C.char)(unsafe.Pointer(&buf[0])), n) == 0 {
			continue
		}
		if dir := strings.TrimSpace(strings.TrimRight(string(buf), "\x00")); dir != "" {
			out = append(out, filepath.Clean(dir))
		}
	}
	return out
})

// darwinUserRoots returns the per-user Darwin temporary and cache directories
// reported by confstr(_CS_DARWIN_USER_TEMP_DIR) and confstr(_CS_DARWIN_USER_CACHE_DIR).
// System shims such as xcrun resolve their scratch state through confstr rather
// than $TMPDIR, so these OS-convention roots remain writable even when TMPDIR
// points elsewhere.
func darwinUserRoots() []string {
	return darwinUserRootsOnce()
}
