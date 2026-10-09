// Package resourceguard checks retained heap and goroutines after a package suite.
package resourceguard

import (
	"fmt"
	"os"
	"runtime"
	"runtime/pprof"
	"time"
)

// Budget bounds retained growth after test cleanup, not transient working memory.
type Budget struct {
	HeapBytes  uint64
	Goroutines int
}

// Run preserves assertion failures and checks successful suites after cleanup settles.
func Run(m interface{ Run() int }, budget Budget) int {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	goroutines := runtime.NumGoroutine()
	code := m.Run()
	if code != 0 {
		return code
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		heapGrowth := uint64(0)
		if after.HeapAlloc > before.HeapAlloc {
			heapGrowth = after.HeapAlloc - before.HeapAlloc
		}
		goroutineGrowth := runtime.NumGoroutine() - goroutines
		if heapGrowth <= budget.HeapBytes && goroutineGrowth <= budget.Goroutines {
			return 0
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "package resource budget exceeded: retained heap %d/%d bytes; goroutines %d/%d\n", heapGrowth, budget.HeapBytes, goroutineGrowth, budget.Goroutines)
			_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 2)
			return 1
		}
		time.Sleep(50 * time.Millisecond)
	}
}
