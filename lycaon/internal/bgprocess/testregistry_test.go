package bgprocess_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
)

// registryCloseBudget bounds teardown so a process that ignores its kill fails
// the test rather than hanging the package.
const registryCloseBudget = 10 * time.Second

// newTestRegistry registers process cleanup with the test.
func newTestRegistry(t *testing.T, cfg bgprocess.Config, hooks bgprocess.Hooks) *bgprocess.Registry {
	t.Helper()
	reg := bgprocess.NewRegistry(cfg, hooks)
	reg.Output.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), registryCloseBudget)
		defer cancel()
		if err := reg.Lifecycle.Close(ctx); err != nil {
			t.Errorf("registry close: %v", err)
		}
	})
	return reg
}
