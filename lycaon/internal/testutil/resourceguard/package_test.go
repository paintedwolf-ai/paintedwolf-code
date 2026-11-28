package resourceguard

import (
	"testing"
	"time"
)

type suiteFunc func() int

func (f suiteFunc) Run() int { return f() }

func TestRunPreservesSourceFailure(t *testing.T) {
	if got := Run(suiteFunc(func() int { return 7 }), Budget{}); got != 7 {
		t.Fatalf("exit = %d, want 7", got)
	}
}

var retainedBySuite []byte

func TestRunAcceptsStoppedGoroutinesAndRetainedMemory(t *testing.T) {
	t.Cleanup(func() { retainedBySuite = nil })
	suite := suiteFunc(func() int {
		done := make(chan struct{})
		go func() { close(done) }()
		<-done
		retainedBySuite = make([]byte, 64<<20)
		return 0
	})
	if got := Run(suite, Budget{}); got != 0 {
		t.Fatalf("suite that stopped its goroutines and kept a cache exit = %d, want 0", got)
	}
}

func TestRunRejectsRetainedGoroutine(t *testing.T) {
	stop, ready := make(chan struct{}), make(chan struct{})
	defer close(stop)
	suite := suiteFunc(func() int { go func() { close(ready); <-stop }(); <-ready; return 0 })
	if got := Run(suite, Budget{Settle: 100 * time.Millisecond}); got != 1 {
		t.Fatalf("leaking suite exit = %d, want 1", got)
	}
}
