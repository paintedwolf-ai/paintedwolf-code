package promptstate

import (
	"runtime"
	"testing"
	"time"
)

func TestOperationLockSerializesMatchingKeys(t *testing.T) {
	m := &State{}
	unlockFirst := m.LockOperation("operation")
	acquired := make(chan struct{})
	go func() {
		unlockSecond := m.LockOperation("operation")
		close(acquired)
		unlockSecond()
	}()

	deadline := time.Now().Add(time.Second)
	for {
		m.operationMu.Lock()
		waiters := m.operationLocks["operation"].refs
		m.operationMu.Unlock()
		if waiters == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second operation lock did not queue")
		}
		runtime.Gosched()
	}
	select {
	case <-acquired:
		t.Fatal("matching operation lock overlapped")
	default:
	}
	unlockFirst()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("matching operation lock did not resume")
	}
}

func TestOperationLockAllowsDistinctKeys(t *testing.T) {
	m := &State{}
	unlockFirst := m.LockOperation("first")
	defer unlockFirst()
	acquired := make(chan struct{})
	go func() {
		unlockSecond := m.LockOperation("second")
		close(acquired)
		unlockSecond()
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("distinct operation lock was blocked")
	}
}
