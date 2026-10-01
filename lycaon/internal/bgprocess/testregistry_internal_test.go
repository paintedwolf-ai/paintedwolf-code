package bgprocess

import (
	"context"
	"testing"
	"time"
)

// newTestRegistry registers process cleanup with the test.
func newTestRegistry(t *testing.T, cfg Config, hooks Hooks) *Registry {
	t.Helper()
	reg := NewRegistry(cfg, hooks)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := reg.Close(ctx); err != nil {
			t.Errorf("registry close: %v", err)
		}
	})
	return reg
}
