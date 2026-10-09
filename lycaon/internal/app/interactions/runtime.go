package interactions

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/browser"
	"github.com/lycaon/lycaon/internal/browser/pagesession"
	"github.com/lycaon/lycaon/internal/browser/preview"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/heldtools"
	"time"
)

type ResourceLifetime interface {
	Track(string, int, func(context.Context) error)
}
type Runtime struct {
	Processes *bgprocess.Registry
	Calls     *heldcall.Registry
	Pages     *pagesession.Registry
	Preview   *preview.Controller
	publisher *events.Publisher
	resources ResourceLifetime
}

func New(publisher *events.Publisher, resources ResourceLifetime) *Runtime {
	return &Runtime{publisher: publisher, resources: resources}
}
func (r *Runtime) Build(completed bgprocess.CompletionPublisher, refused bgprocess.RefusalPublisher, settled func(string, string), cleanup func(string, int, func(context.Context, string) error) error) error {
	r.Processes = bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{Publish: r.publisher.PublishProcess, Complete: completed, Refused: refused})
	processes := r.Processes
	r.resources.Track("background-processes", 50, func(ctx context.Context) error {
		closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return processes.Lifecycle.Close(closeCtx)
	})
	r.Calls = heldcall.New(r.publisher.PublishProcess, settled)
	calls := r.Calls
	r.resources.Track("held-calls", 49, func(ctx context.Context) error {
		closeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return calls.Close(closeCtx)
	})
	r.Pages = pagesession.NewRegistry(pagesession.DefaultConfig())
	pages := r.Pages
	r.resources.Track("browser-pages", 40, func(ctx context.Context) error { pages.Close(ctx); return nil })
	r.Preview = preview.NewController(preview.DefaultConfig(), r.publisher.PublishPreview)
	controller := r.Preview
	r.resources.Track("preview", 30, func(context.Context) error { controller.Close(); return nil })
	if err := cleanup("preview-streams", 40, controller.DisposeSession); err != nil {
		return fmt.Errorf("register preview cleanup: %w", err)
	}
	pages.SetOnClose(func(sessionID, pageID string) { controller.Detach(context.Background(), sessionID, pageID) })
	return nil
}
func (r *Runtime) RegisterTools(reg *tools.DefaultRegistry, pool *browser.Pool) error {
	if err := heldtools.Register(reg, r.Calls); err != nil {
		return fmt.Errorf("held call tools: %w", err)
	}
	if err := native.RegisterTerminalSessionTools(reg, r.Processes); err != nil {
		return fmt.Errorf("terminal session tools: %w", err)
	}
	if pool == nil {
		return nil
	}
	if err := native.RegisterCapturePageTool(reg, pool, r.Processes, r.Preview); err != nil {
		return fmt.Errorf("capture_page tool: %w", err)
	}
	if err := native.RegisterMeasurePageTool(reg, pool, r.Pages, r.Processes); err != nil {
		return fmt.Errorf("measure_page tool: %w", err)
	}
	if err := native.RegisterPageSessionTools(reg, pool, r.Pages, r.Processes, r.Preview); err != nil {
		return fmt.Errorf("page session tools: %w", err)
	}
	return nil
}
