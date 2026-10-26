package scantest

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestRunServiceStopsOnCleanup(t *testing.T) {
	var stopped atomic.Bool
	t.Run("service", func(t *testing.T) {
		RunService(t, func(ctx context.Context) error {
			<-ctx.Done()
			stopped.Store(true)
			return ctx.Err()
		})
	})
	if !stopped.Load() {
		t.Fatal("service was still running after cleanup")
	}
}
