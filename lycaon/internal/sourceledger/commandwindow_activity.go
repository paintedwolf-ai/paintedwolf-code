package sourceledger

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// windowSettlePassBudget bounds the pass that ends a settle grace or a close.
const windowSettlePassBudget = 45 * time.Second

// Hold marks one external process of the window's program as live, so the window
// may claim drift until the returned release and its settle grace. A window that
// has gone idle re-anchors first, so drift from the gap is not claimed; the wait
// ends with ctx, and an error means this process runs unobserved.
func (o *OpenCommandWindow) Hold(ctx context.Context) (func(), error) {
	if o == nil || o.store == nil {
		return func() {}, nil
	}
	return o.store.holdWindow(ctx, o.window)
}

// ReleaseOpener gives up the hold the window opened with, for a program whose
// launches hold it themselves. Commands that run for the window's whole life keep
// it until Close.
func (o *OpenCommandWindow) ReleaseOpener(ctx context.Context) {
	if o == nil || o.store == nil {
		return
	}
	o.opener.Do(func() { o.store.releaseHold(ctx, o.window) })
}

// windowAnchor is one re-anchor run; err is set before done closes.
type windowAnchor struct {
	done chan struct{}
	err  error
}

func (s *Commands) holdWindow(ctx context.Context, w *openCommandWindow) (func(), error) {
	s.windowsMu.Lock()
	if !w.closing && !w.active {
		anchor := w.anchoring
		if anchor == nil {
			anchor = &windowAnchor{done: make(chan struct{})}
			w.anchoring = anchor
			go s.reanchorWindow(context.WithoutCancel(ctx), w, anchor)
		}
		s.windowsMu.Unlock()
		select {
		case <-anchor.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if anchor.err != nil {
			return nil, anchor.err
		}
		s.windowsMu.Lock()
		// The anchor's own waiters activate the window, so with none left it stays idle.
		w.active = !w.closing
	}
	defer s.windowsMu.Unlock()
	if w.closing {
		return nil, ErrWindowClosed
	}
	w.holds++
	endGrace(w)
	var once sync.Once
	return func() { once.Do(func() { s.releaseHold(ctx, w) }) }, nil
}

// reanchorWindow moves an idle window's start to the present before it claims
// again; anything earlier was the gap. It runs detached and bounded, so a launch
// that stops waiting does not cost later launches their window.
func (s *Commands) reanchorWindow(ctx context.Context, w *openCommandWindow, anchor *windowAnchor) {
	budget, cancel := context.WithTimeout(ctx, s.observationBudget)
	defer cancel()
	snapshotID, err := s.anchorStart(budget, w.projectID, w.roots, false)
	if err == nil {
		w.moveStart(snapshotID)
		if retainErr := s.retainWindowStart(budget, w); retainErr != nil {
			slog.WarnContext(ctx, "command window start retention", "window_id", w.id, "err", retainErr)
		}
	}
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	w.anchoring, anchor.err = nil, err
	close(anchor.done)
}

// releaseHold starts the settle grace when the last live process has ended.
func (s *Commands) releaseHold(ctx context.Context, w *openCommandWindow) {
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	w.holds--
	if w.holds > 0 || w.closing || !w.active {
		return
	}
	quiet := make(chan struct{})
	w.quiet = quiet
	go s.quiesceWindow(context.WithoutCancel(ctx), w, quiet)
}

// endGrace stops a settle grace in progress. Caller holds windowsMu.
func endGrace(w *openCommandWindow) {
	if w.quiet != nil {
		close(w.quiet)
		w.quiet = nil
	}
}

// quiesceWindow ends a settle grace the way a close does: wait for trailing
// changes, run the pass that lands them while the window may still claim them,
// and only then stop claiming. A new hold or the close takes over at any point.
func (s *Commands) quiesceWindow(ctx context.Context, w *openCommandWindow, quiet chan struct{}) {
	s.awaitSettlePeriod(ctx, quiet)
	select {
	case <-quiet:
		return
	default:
	}
	budget, cancel := context.WithTimeout(ctx, windowSettlePassBudget)
	defer cancel()
	release, passErr, lockErr := s.settleWindowObservations(budget, w)
	if release != nil {
		defer release()
	}
	s.windowsMu.Lock()
	defer s.windowsMu.Unlock()
	if w.quiet != quiet {
		return
	}
	w.quiet, w.active = nil, false
	if passErr != nil || lockErr != nil {
		w.incomplete = true
		slog.WarnContext(ctx, "command window settle grace", "window_id", w.id, "pass_err", passErr, "lock_err", lockErr)
	}
	if s.windowIdled != nil {
		s.windowIdled(w.id)
	}
}
