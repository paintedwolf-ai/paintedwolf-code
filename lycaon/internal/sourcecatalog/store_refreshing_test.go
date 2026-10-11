package sourcecatalog

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
)

// Refreshing promises a newer generation. A pass that has published its last
// generation is still running, but nothing newer is coming unless its epoch
// asked for another pass.
func TestRefreshingMeansANewerGenerationIsPending(t *testing.T) {
	root := Root{ID: "r", Path: t.TempDir()}
	pass := func() *storeCore {
		core := &storeCore{root: root, building: true}
		core.status = TreeStatus{State: StateWarming}
		return core
	}
	published := TreeStatus{State: StateReady, Revision: 1, Complete: true}

	running := pass()
	if !running.refreshingLocked() {
		t.Fatal("a pass that has not published its last generation is refreshing")
	}

	settled := pass()
	settled.publishFinalLocked(published, repochange.CurrentEpoch(root.Path))
	if settled.refreshingLocked() {
		t.Fatal("the pass's own final generation reported a newer one behind it")
	}

	overtaken := pass()
	epoch := repochange.CurrentEpoch(root.Path)
	repochange.Advance(root.Path)
	overtaken.publishFinalLocked(published, epoch)
	if !overtaken.refreshingLocked() {
		t.Fatal("a write that overtook the pass must ask for a newer generation")
	}

	changed := pass()
	changed.publishFinalLocked(published, repochange.CurrentEpoch(root.Path))
	changed.recordChanges([]string{"a.go"})
	if !changed.refreshingLocked() {
		t.Fatal("a change after publication must ask for a newer generation")
	}
}

// tailStore publishes each pass's final generation, then holds the pass in its
// tail (the index WAL checkpoint) until the test releases it.
type tailStore struct {
	storeCore
	passes      chan repochange.Epoch
	tail        chan struct{}
	releaseTail func()
}

func newTailStore(t *testing.T) *tailStore {
	t.Helper()
	s := &tailStore{passes: make(chan repochange.Epoch, 4), tail: make(chan struct{})}
	s.releaseTail = sync.OnceFunc(func() { close(s.tail) })
	s.root = Root{ID: "r", Path: t.TempDir()}
	s.status = TreeStatus{State: StateReady, Revision: 1, Complete: true}
	t.Cleanup(func() {
		s.releaseTail()
		if done := s.passDone(); done != nil {
			<-done
		}
	})
	return s
}

func (s *tailStore) schema() string                           { return "" }
func (s *tailStore) workKey() string                          { return "tail" }
func (s *tailStore) scopeChanges(c []string) ([]string, bool) { return c, true }
func (s *tailStore) contentBuilds() []*contentBuild           { return nil }
func (s *tailStore) partial() bool                            { return false }

func (s *tailStore) reconcile(_ context.Context, epoch repochange.Epoch) error {
	s.takeWork()
	s.mu.Lock()
	s.publishFinalLocked(TreeStatus{State: StateReady, Revision: s.status.Revision + 1, Complete: true, ValidatedAt: time.Now()}, epoch)
	s.mu.Unlock()
	s.passes <- epoch
	<-s.tail
	return nil
}

func (s *tailStore) passDone() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done
}

// startTailPass starts a pass and returns once it has published its final
// generation and entered its tail.
func startTailPass(t *testing.T, s *tailStore) repochange.Epoch {
	t.Helper()
	s.full = true
	if _, err := openStore(t.Context(), s, 0, nil); err != nil {
		t.Fatalf("open store: %v", err)
	}
	return <-s.passes
}

// finishTail releases the tail and waits for reconciliation to end.
func finishTail(t *testing.T, s *tailStore) {
	t.Helper()
	s.releaseTail()
	select {
	case <-s.passDone():
	case <-t.Context().Done():
		t.Fatal("reconciliation did not end")
	}
}

// A write that lands after a pass settled, while the pass still runs its tail,
// is newer than the published generation: a read reports it as refreshing and
// the running pass builds it before it exits.
func TestWriteDuringSettledPassTailRequestsNewerGeneration(t *testing.T) {
	s := newTailStore(t)
	first := startTailPass(t, s)

	repochange.Advance(s.root.Path)
	status, err := openStore(t.Context(), s, 0, nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if !status.Refreshing {
		t.Fatalf("status after write = %+v, want refreshing behind the settled generation", status)
	}

	finishTail(t, s)
	if len(s.passes) != 1 {
		t.Fatalf("the write reached %d passes, want 1", len(s.passes))
	}
	if second := <-s.passes; second.Value <= first.Value || !repochange.EpochCurrent(s.root.Path, second) {
		t.Fatalf("second pass observed epoch %d after first %d, want the current epoch", second.Value, first.Value)
	}
	status, err = openStore(t.Context(), s, 0, nil)
	if err != nil || status.Refreshing || status.Revision != 3 {
		t.Fatalf("status after second pass = %+v, %v; want revision 3 settled", status, err)
	}
}

// A change the index does not represent, delivered while a settled pass runs its
// tail, leaves the published generation current: no second pass follows.
func TestUnrepresentedChangeDuringSettledPassTailKeepsGeneration(t *testing.T) {
	s := newTailStore(t)
	c := &Catalog{Trees: &TreeStores{trees: map[string]projectionStore{"tail": s}}}
	startTailPass(t, s)

	repochange.Advance(s.root.Path)
	c.Trees.observeTreeEpoch(s.root.Path)
	status, err := openStore(t.Context(), s, 0, nil)
	if err != nil || status.Refreshing {
		t.Fatalf("status after unrepresented change = %+v, %v; want the settled generation current", status, err)
	}
	finishTail(t, s)
	if len(s.passes) != 0 {
		t.Fatalf("an unrepresented change started %d more passes", len(s.passes))
	}
	status, err = openStore(t.Context(), s, 0, nil)
	if err != nil || status.Refreshing {
		t.Fatalf("status after the pass ended = %+v, %v; want no pass for an unrepresented change", status, err)
	}
}
