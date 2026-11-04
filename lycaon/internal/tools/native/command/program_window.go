package command

import (
	"context"
	"errors"
	"sync"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/tools"
)

// programWindow is one program's observation window, opened by whichever
// external launch reaches it first and shared by every later one, including
// concurrent pipeline stages, nested commands and & jobs. It claims drift only
// while one of those launches holds it.
type programWindow struct {
	// due is false where commands are not observed, so launches never wait.
	due    bool
	open   func(context.Context) commandWindow
	clock  commandprogram.Clock
	once   sync.Once
	cancel context.CancelFunc
	// ready closes once window is written and the program may launch.
	ready  chan struct{}
	window commandWindow

	mu sync.Mutex
	// waiting counts launches on their way to a hold; the opener's hold keeps
	// the window active for them, so it goes once they have theirs.
	waiting int
	opened  bool
	// missed means a launch ran without the window watching it.
	missed bool
}

func newProgramWindow(tc tools.ToolContext, commandLine string, clock commandprogram.Clock) *programWindow {
	return &programWindow{
		due:   commandWindowDue(tc),
		open:  func(ctx context.Context) commandWindow { return openCommandWindow(ctx, tc, "command", commandLine) },
		clock: clock,
		ready: make(chan struct{}),
	}
}

// enter returns once the launch may start, holding the window until release. An
// error means the launch's own context ended. Observation never takes time the
// program assigned to its command: a launch with a bound waits at most half of
// what remains, then runs unobserved. The open outlives the launch that began it,
// so a stage's bound does not cost later launches their window, and a job may
// launch after the call's context ended.
func (w *programWindow) enter(ctx context.Context) (func(), error) {
	if w == nil || !w.due {
		return func() {}, nil
	}
	w.mu.Lock()
	w.waiting++
	w.mu.Unlock()
	defer w.leave(ctx)
	w.once.Do(func() {
		openCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		w.cancel = cancel
		go func() {
			defer close(w.ready)
			w.window = w.open(openCtx)
			w.mu.Lock()
			w.opened = true
			w.mu.Unlock()
			w.releaseOpenerIfUnwaited(ctx)
		}()
	})
	waitCtx, stop := w.boundedWait(ctx)
	defer stop()
	select {
	case <-w.ready:
	case <-waitCtx.Done():
		return w.unobserved(ctx)
	}
	if w.window.open == nil {
		return func() {}, nil
	}
	release, err := w.window.open.Hold(waitCtx)
	if err != nil {
		return w.unobserved(ctx)
	}
	return release, nil
}

// boundedWait is ctx, ending early at half of any bound on the launch.
func (w *programWindow) boundedWait(ctx context.Context) (context.Context, func()) {
	remaining, bounded := commandprogram.RemainingBound(ctx)
	if !bounded {
		return ctx, func() {}
	}
	waitCtx, cancel := context.WithCancelCause(ctx)
	stopTimer := w.clock.AfterFunc(remaining/2, func() { cancel(errors.New("window wait bound reached")) })
	return waitCtx, func() {
		stopTimer()
		cancel(nil)
	}
}

// unobserved lets a launch run without the window, unless its context ended.
func (w *programWindow) unobserved(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.missed = true
	return func() {}, nil
}

func (w *programWindow) leave(ctx context.Context) {
	w.mu.Lock()
	w.waiting--
	w.mu.Unlock()
	w.releaseOpenerIfUnwaited(ctx)
}

func (w *programWindow) releaseOpenerIfUnwaited(ctx context.Context) {
	w.mu.Lock()
	unwaited := w.opened && w.waiting == 0
	w.mu.Unlock()
	if unwaited {
		w.window.open.ReleaseOpener(ctx)
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

// fact states the window on the program's result. An open still in flight has
// nothing to state yet, unless a launch has already gone ahead without it.
func (w *programWindow) fact() *hostcmd.SourceWindow {
	var fact *hostcmd.SourceWindow
	select {
	case <-w.ready:
		fact = w.window.fact()
	default:
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.missed {
		return fact
	}
	if fact == nil {
		return &hostcmd.SourceWindow{Unobserved: true}
	}
	fact.Unobserved = true
	return fact
}
