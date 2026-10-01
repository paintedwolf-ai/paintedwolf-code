package sourcecatalog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRecursiveWaitRetainsCompletionSupersededBeforeHandoff(t *testing.T) {
	catalog, root := indexFixture(t)
	store, err := catalog.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "get completion handoff store", err)
	done := make(chan struct{})
	var stop sync.Once
	store.mu.Lock()
	store.inventory = inventoryWork{cancel: func() { stop.Do(func() { close(done) }) }, done: done, wake: make(chan struct{}, 1), changed: make(chan struct{}), running: true}
	store.mu.Unlock()
	deadline, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	waiting := &structuralWaitingContext{Context: deadline, waiting: make(chan struct{})}
	result := make(chan error, 1)
	go func() { result <- catalog.observeSubtree(waiting, "p", root, ".") }()
	select {
	case <-waiting.waiting:
	case <-deadline.Done():
		t.Fatal("recursive waiter did not start")
	}
	publish := func(names []string, directories bool) {
		t.Helper()
		pin, err := store.retainGeneration(headGeneration, true)
		testutil.FailErr(t, "pin publication basis", err)
		defer pin.Release()
		builder, err := newStructuralBuilder(store, pin.value)
		testutil.FailErr(t, "create handoff publication", err)
		defer builder.close()
		observePublicationFixture(t, builder, store, ".", names, directories)
		testutil.FailErr(t, "publish handoff structure", store.publishStructure(t.Context(), builder, pin.Generation))
	}
	publish([]string{"known.txt"}, false)
	completed, err := catalog.OpenCompletedNavigation(t.Context(), "p", root)
	testutil.FailErr(t, "retain completed result", err)
	defer func() { _ = completed.Close() }()
	publish([]string{"unknown"}, true)
	store.mu.Lock()
	store.inventory.running = false
	close(store.inventory.changed)
	store.inventory.changed = make(chan struct{})
	store.mu.Unlock()
	testutil.FailErr(t, "settle superseded completion", <-result)
	_, err = completed.Entry(t.Context(), "known.txt")
	testutil.FailErr(t, "read exact completed membership", err)
	testutil.FailErr(t, "drain completed ownership", catalog.Drain(t.Context()))
	continued, err := completed.pin.OpenNavigation(t.Context())
	testutil.FailErr(t, "continue retained completion after drain", err)
	_ = continued.Close()
	_, err = catalog.OpenCompletedNavigation(t.Context(), "p", root)
	if !errors.Is(err, pagedview.ErrExpired) {
		t.Fatalf("drained completed lookup=%v", err)
	}
}
