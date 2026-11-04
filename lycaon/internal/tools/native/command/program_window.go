package command

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/tools"
)

// programWindow is one program's observation window, opened by whichever
// external launch reaches it first and shared by every later one, including
// concurrent pipeline stages, nested commands and & jobs.
type programWindow struct {
	open   func(context.Context) commandWindow
	once   sync.Once
	cancel context.CancelFunc
	// ready closes once window is written and the program may launch.
	ready  chan struct{}
	window commandWindow
}

func newProgramWindow(tc tools.ToolContext, commandLine string) *programWindow {
	return &programWindow{
		open:  func(ctx context.Context) commandWindow { return openCommandWindow(ctx, tc, "command", commandLine) },
		ready: make(chan struct{}),
	}
}

// ensure returns once the window's open has finished, whatever its outcome, or
// when this launch's own context ends. The open outlives the launch that began
// it: a stage's timeout must not cost the program's later launches their
// window, and a job may launch after the call's context ended.
func (w *programWindow) ensure(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		openCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		w.cancel = cancel
		go func() {
			defer close(w.ready)
			w.window = w.open(openCtx)
		}()
	})
	select {
	case <-w.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// settle ends the window once the program and its jobs have ended. An open
// still in flight has no launch left to serve, so it is abandoned.
func (w *programWindow) settle(ctx context.Context) {
	w.once.Do(func() { close(w.ready) })
	if w.cancel != nil {
		w.cancel()
	}
	<-w.ready
	w.window.closeNow(ctx)
}

// fact states the window on the program's result; an open still in flight has
// nothing to state yet.
func (w *programWindow) fact() *hostcmd.SourceWindow {
	select {
	case <-w.ready:
		return w.window.fact()
	default:
		return nil
	}
}
