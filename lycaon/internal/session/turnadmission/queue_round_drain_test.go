package turnadmission

import (
	"context"
	"testing"
)

// A pass that starts while wait is blocked holds wait until it finishes too.
func TestRoundEndDrainWaitCoversPassStartedDuringWait(t *testing.T) {
	var drains roundEndDrains
	_, first := drains.start(context.Background(), "s1")
	waited := make(chan struct{})
	go func() {
		drains.wait(context.Background())
		close(waited)
	}()
	_, second := drains.start(context.Background(), "s2")
	drains.finish("s1", first)
	select {
	case <-waited:
		t.Fatal("wait returned while the second pass was still running")
	default:
	}
	drains.finish("s2", second)
	<-waited
}

func TestRoundEndDrainWaitCancelsPassesWhenContextEnds(t *testing.T) {
	var drains roundEndDrains
	passCtx, work := drains.start(context.Background(), "s1")
	go func() {
		<-passCtx.Done()
		drains.finish("s1", work)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	drains.wait(ctx)
	if passCtx.Err() == nil {
		t.Fatal("wait returned without canceling the running pass")
	}
}
