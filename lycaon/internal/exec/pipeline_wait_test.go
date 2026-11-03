package exec

import (
	"errors"
	"testing"
)

func TestPipelineWaitObservationChecksReadinessAndWakesBeforeCompletion(t *testing.T) {
	pipeline := &AsyncPipeline{done: make(chan struct{})}
	blocked, woken := 0, 0
	release, err := pipeline.ObserveWait(func() (func(), error) {
		blocked++
		return func() {
			select {
			case <-pipeline.Done():
				t.Error("completion became visible before its waiter woke")
			default:
			}
			woken++
		}, nil
	})
	if err != nil {
		t.Fatalf("observe pipeline wait: %v", err)
	}
	pipeline.complete()
	release()
	release()
	if blocked != 1 || woken != 1 {
		t.Fatalf("blocked=%d woken=%d", blocked, woken)
	}
	release, err = pipeline.ObserveWait(func() (func(), error) { t.Fatal("already completed pipeline reported blocked"); return nil, nil })
	if err != nil {
		t.Fatalf("observe ready pipeline: %v", err)
	}
	release()
}
func TestPipelineWaitObservationReleasesOrRefusesBeforeCompletion(t *testing.T) {
	pipeline := &AsyncPipeline{done: make(chan struct{})}
	woken := 0
	release, err := pipeline.ObserveWait(func() (func(), error) { return func() { woken++ }, nil })
	if err != nil {
		t.Fatalf("observe pipeline: %v", err)
	}
	release()
	release()
	refused := errors.New("wait refused")
	if _, err := pipeline.ObserveWait(func() (func(), error) { return nil, refused }); !errors.Is(err, refused) {
		t.Fatalf("refusal %v", err)
	}
	pipeline.complete()
	if woken != 1 {
		t.Fatalf("released waiter woke %d times", woken)
	}
}
