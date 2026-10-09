package registry_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/scan"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/scan/registry"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type closingScanner struct {
	id     string
	closed atomic.Int32
}

func (s *closingScanner) ID() string                     { return s.id }
func (s *closingScanner) Categories() []api.ScanCategory { return nil }
func (s *closingScanner) Run(context.Context, scan.ScanRequest) (*scanoutput.Result, error) {
	return &scanoutput.Result{}, nil
}
func (s *closingScanner) Close() error { s.closed.Add(1); return nil }

func TestCloseStopsEveryAdapter(t *testing.T) {
	reg, err := registry.New(registry.Options{ModuleRoot: configlayout.FindModuleRoot(), HomeDir: t.TempDir()})
	testutil.FailErr(t, "registry.New", err)
	adapter := &closingScanner{id: "closing-fixture"}
	testutil.FailErr(t, "register adapter", reg.Register(adapter))

	testutil.FailErr(t, "close registry", reg.Close())
	if adapter.closed.Load() != 1 {
		t.Fatalf("adapter closed %d times, want 1", adapter.closed.Load())
	}
}

func TestReloadStopsReplacedAdapters(t *testing.T) {
	reg, err := registry.New(registry.Options{ModuleRoot: configlayout.FindModuleRoot(), HomeDir: t.TempDir()})
	testutil.FailErr(t, "registry.New", err)
	adapter := &closingScanner{id: "closing-fixture"}
	testutil.FailErr(t, "register adapter", reg.Register(adapter))

	testutil.FailErr(t, "reload", reg.Reload())
	testutil.WaitFor(t, 5*time.Second, func() bool { return adapter.closed.Load() == 1 })
	testutil.FailErr(t, "close registry", reg.Close())
}
