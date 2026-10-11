package session

import (
	"sync"
	"testing"
	"time"
)

func TestTryIdleMutationSerializesWithSessionLock(t *testing.T) {
	m := newForgetTestManager(t)
	unlock, ok := m.Runner.Execution.TryIdleMutation("sess-1")
	if !ok || unlock == nil {
		t.Fatal("expected idle mutation lock")
	}

	if _, ok := m.Runner.Execution.TryIdleMutation("sess-1"); ok {
		t.Fatal("second TryIdleMutation must fail while held")
	}

	held := make(chan struct{})
	released := make(chan struct{})
	go func() {
		lock := m.Runner.Execution.Prompt.Acquire("sess-1")
		close(held)
		lock.Lock()
		defer lock.Unlock()
		close(released)
	}()
	select {
	case <-held:
	case <-time.After(time.Second):
		t.Fatal("sessionLock waiter did not start")
	}
	select {
	case <-released:
		t.Fatal("sessionLock must block while idle mutation holds the lock")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("sessionLock did not acquire after idle mutation release")
	}
}

func TestTryIdleMutationSameMutexAsPrompt(t *testing.T) {
	m := newForgetTestManager(t)
	unlock, ok := m.Runner.Execution.TryIdleMutation("sess-1")
	if !ok {
		t.Fatal("expected lock")
	}
	var wg sync.WaitGroup
	wg.Add(1)
	blocked := make(chan struct{})
	go func() {
		defer wg.Done()
		close(blocked)
		lock := m.Runner.Execution.Prompt.Acquire("sess-1")
		lock.Lock()
		defer lock.Unlock()
	}()
	<-blocked
	time.Sleep(20 * time.Millisecond)
	unlock()
	wg.Wait()
}
