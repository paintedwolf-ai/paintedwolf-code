package native

import (
	"testing"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/secretmatch"
)

func newTestBackgroundRegistry(t *testing.T) *bgprocess.Registry {
	t.Helper()
	registry := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	registry.Output.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	return registry
}
