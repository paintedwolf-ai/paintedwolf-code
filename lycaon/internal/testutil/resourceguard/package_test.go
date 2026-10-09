package resourceguard

import (
	"runtime"
	"testing"
)

type suiteFunc func() int

func (f suiteFunc) Run() int { return f() }

func TestRunPreservesSourceFailure(t *testing.T) {
	if got := Run(suiteFunc(func() int { return 7 }), Budget{}); got != 7 {
		t.Fatalf("exit = %d, want 7", got)
	}
}

func TestRunAcceptsReleasedResources(t *testing.T) {
	suite := suiteFunc(func() int {
		done := make(chan struct{})
		go func() { close(done) }()
		<-done
		memory := make([]byte, 1<<20)
		runtime.KeepAlive(memory)
		return 0
	})
	if got := Run(suite, Budget{HeapBytes: 1 << 20, Goroutines: 2}); got != 0 {
		t.Fatalf("released suite exit = %d", got)
	}
}

func TestRunRejectsRetainedGoroutine(t *testing.T) {
	stop, ready := make(chan struct{}), make(chan struct{})
	defer close(stop)
	suite := suiteFunc(func() int { go func() { close(ready); <-stop }(); <-ready; return 0 })
	if got := Run(suite, Budget{HeapBytes: 1 << 20}); got != 1 {
		t.Fatalf("leaking suite exit = %d, want 1", got)
	}
}
