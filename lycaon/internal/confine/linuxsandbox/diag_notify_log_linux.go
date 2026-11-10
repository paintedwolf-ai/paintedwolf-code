//go:build linux

package linuxsandbox

import (
	"fmt"
	"os"
	"sync"
)

// diagNotifyMu serializes diagnostic writes to stderr.
var diagNotifyMu sync.Mutex

// diagNotifyf writes one diagnostic supervisor line to stderr. Temporary
// instrumentation for the upgrade-corpus glycin investigation.
func diagNotifyf(format string, args ...any) {
	diagNotifyMu.Lock()
	defer diagNotifyMu.Unlock()
	fmt.Fprintf(os.Stderr, "PWDIAG "+format+"\n", args...)
}
