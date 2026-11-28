// Package resourceguard checks for goroutines a package suite leaves running.
//
// Retained heap is not checked here: a byte threshold cannot tell a leak from a
// bounded cache. Leaks of owned objects are caught by reachability checks on
// those objects, and runaway memory by the per-process RSS ceiling.
package resourceguard

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"
)

// Budget bounds goroutines still running after test cleanup.
type Budget struct {
	Goroutines int
	// Settle is how long cleanup may keep stopping goroutines before the
	// remainder counts as leaked; zero means one minute.
	Settle time.Duration
}

// Run preserves assertion failures and checks successful suites after cleanup settles.
func Run(m interface{ Run() int }, budget Budget) int {
	goroutines := runtime.NumGoroutine()
	code := m.Run()
	if code != 0 {
		return code
	}
	settle := budget.Settle
	if settle == 0 {
		settle = time.Minute
	}
	deadline := time.Now().Add(settle)
	for {
		growth := runtime.NumGoroutine() - goroutines
		if growth <= budget.Goroutines {
			return 0
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "package resource budget exceeded: goroutines %d/%d\n", growth, budget.Goroutines)
			_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
			return 1
		}
		time.Sleep(50 * time.Millisecond)
	}
}
