package nativegen

import (
	"context"
	"sync"
	"time"
)

const stepCancellationGrace = 2 * time.Second

// Cancellation retains a responsive model and reaps stalled work.
func (p *rpcProcess) watchCancellation(ctx context.Context, step bool) func() bool {
	settled, finished := make(chan struct{}), make(chan struct{})
	forced := false
	go func() {
		defer close(finished)
		select {
		case <-settled:
			return
		case <-ctx.Done():
		}
		if step {
			timer := time.NewTimer(stepCancellationGrace)
			defer timer.Stop()
			select {
			case <-settled:
				return
			case <-timer.C:
			}
		}
		forced = true
		p.kill()
	}()
	var once sync.Once
	return func() bool {
		once.Do(func() { close(settled) })
		<-finished
		return forced
	}
}
