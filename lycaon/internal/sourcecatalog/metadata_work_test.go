package sourcecatalog

import (
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMetadataDiscoveryYieldsItsLaneBetweenBatches(t *testing.T) {
	broker := backgroundwork.New(map[backgroundwork.Resource]backgroundwork.Limits{backgroundwork.ResourceMetadata: {Total: 1, PerLane: 1}})
	request := backgroundwork.Request{Lane: "root", Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}}
	ctx, release, err := admitMetadata(t.Context(), broker, request)
	testutil.FailErr(t, "admit discovery", err)
	defer release()
	work := ctx.Value(metadataWorkKey{}).(*metadataWork)
	yielded, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(resume) })
	initialRelease := work.release
	work.release = func() {
		initialRelease()
		other, acquireErr := broker.Acquire(t.Context(), request)
		if acquireErr != nil {
			return
		}
		close(yielded)
		<-resume
		other()
	}
	finished := make(chan error, 1)
	go func() {
		for range metadataWorkBatch {
			if err := nextMetadataEntry(ctx); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	select {
	case <-yielded:
	case err := <-finished:
		t.Fatalf("discovery did not yield its metadata lane: %v", err)
	}
	once.Do(func() { close(resume) })
	testutil.FailErr(t, "continue discovery", <-finished)
}
