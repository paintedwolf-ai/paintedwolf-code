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
