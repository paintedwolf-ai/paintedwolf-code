package registry_test

import (
	"context"
	"errors"
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
	testutil.FailErr(t, "close registry again", reg.Close())
	if !errors.Is(reg.Reload(), context.Canceled) {
		t.Fatal("closed registry admitted a new generation")
	}
	if !errors.Is(reg.Register(&closingScanner{id: "after-close"}), context.Canceled) {
		t.Fatal("closed registry admitted another adapter")
	}
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

type retiringScanner struct {
	closingScanner
	entered  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (s *retiringScanner) Retire(ctx context.Context) error {
	close(s.entered)
	<-ctx.Done()
	close(s.canceled)
	<-s.release
	return s.Close()
}

func TestCloseJoinsRetiringGeneration(t *testing.T) {
	reg, err := registry.New(registry.Options{ModuleRoot: configlayout.FindModuleRoot(), HomeDir: t.TempDir()})
	testutil.FailErr(t, "registry.New", err)
	adapter := &retiringScanner{closingScanner: closingScanner{id: "retiring-fixture"}, entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	testutil.FailErr(t, "register adapter", reg.Register(adapter))
	testutil.FailErr(t, "reload", reg.Reload())
	<-adapter.entered
	closed := make(chan error, 1)
	go func() { closed <- reg.Close() }()
	<-adapter.canceled
	select {
	case err := <-closed:
		t.Fatalf("close returned before retiring adapter drained: %v", err)
	default:
	}
	close(adapter.release)
	testutil.FailErr(t, "close registry", <-closed)
	if adapter.closed.Load() != 1 {
		t.Fatalf("retired adapter closed %d times", adapter.closed.Load())
	}
}
