package policyindex

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/governance"
)

// Concurrent warms of one session's shared state collapse into a single
// filesystem walk, with every caller observing its result.
func TestEnsureAgentsMDStateCollapsesConcurrentWarmsForSharedState(t *testing.T) {
	const sid = "sess-agents-md-race"
	const workspacePath = "/workspace/does-not-matter"

	m := &Service{}
	m.cache.Store(sid, &governance.AgentsMDSessionState{RootPath: workspacePath})

	var calls int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	built := []governance.ResolvedAgentsMD{{Path: "AGENTS.md"}}
	m.listIndex = func(root string) ([]governance.ResolvedAgentsMD, error) {
		if root != workspacePath {
			t.Errorf("listIndex called with root %q, want %q", root, workspacePath)
		}
		if atomic.AddInt32(&calls, 1) == 1 {
			entered <- struct{}{}
			<-release
		}
		return built, nil
	}

	const n = 5
	results := make([]*governance.AgentsMDSessionState, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = m.ensureState(t.Context(), sid, workspacePath)
		}(i)
	}

	<-entered
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("listIndex called %d times, want 1 (concurrent warms must collapse)", got)
	}
	for i, state := range results {
		index := state.Snapshot()
		if len(index) != 1 || index[0].Path != "AGENTS.md" {
			t.Errorf("goroutine %d: got index %+v, want the single built entry", i, index)
		}
	}
}

// Concurrent SetIndex/Snapshot on a shared state are race-free.
func TestAgentsMDSessionStateSnapshotSetConcurrentSafe(t *testing.T) {
	state := &governance.AgentsMDSessionState{RootPath: "/workspace"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			state.SetIndex([]governance.ResolvedAgentsMD{{Path: "AGENTS.md"}})
			_ = state.Snapshot()
		}(i)
	}
	wg.Wait()
	index := state.Snapshot()
	if len(index) != 1 {
		t.Fatalf("got %d index entries, want 1", len(index))
	}
}

func TestAgentsMDIndexCachesEmptyResultAndRetriesFailure(t *testing.T) {
	m := &Service{}
	calls := 0
	m.listIndex = func(string) ([]governance.ResolvedAgentsMD, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("walk unavailable")
		}
		return nil, nil
	}
	first := m.ensureState(t.Context(), "session", "/root")
	if first.Indexed() {
		t.Fatal("failed walk became authoritative")
	}
	second := m.ensureState(t.Context(), "session", "/root")
	if first != second || !second.Indexed() {
		t.Fatal("empty index was not retained")
	}
	m.ensureState(t.Context(), "session", "/root")
	if calls != 2 {
		t.Fatalf("empty workspace was walked again: %d calls", calls)
	}
	m.ensureState(t.Context(), "session", "/different-root")
	if calls != 3 {
		t.Fatal("root change reused another workspace index")
	}
	m.Forget("session")
	m.ensureState(t.Context(), "session", "/different-root")
	if calls != 4 {
		t.Fatal("session invalidation retained index")
	}
}
