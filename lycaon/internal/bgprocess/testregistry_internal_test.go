package bgprocess

import (
	"context"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"testing"
	"time"
)

// newTestRegistry registers process cleanup with the test.
func newTestRegistry(t *testing.T, cfg Config, hooks Hooks) *Registry {
	t.Helper()
	reg := NewRegistry(cfg, hooks)
	reg.Output.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := reg.Lifecycle.Close(ctx); err != nil {
			t.Errorf("registry close: %v", err)
		}
	})
	return reg
}
