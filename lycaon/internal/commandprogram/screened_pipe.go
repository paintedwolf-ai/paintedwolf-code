package commandprogram

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/lycaon/lycaon/internal/captureprojection"
)

// ScreenedPipe joins the capture owner's bounded screening to a backpressured
// byte endpoint. Raw source bytes never enter the pipe or its retained output.
// Source.Close must unblock a pending Read, as with a process output pipe.
type ScreenedPipe struct {
	pipe        *BytePipe
	source      io.ReadCloser
	done        chan struct{}
	once        sync.Once
	sourceOnce  sync.Once
	sourceError error
	mu          sync.Mutex
	settled     bool
	cancel      context.CancelCauseFunc
	metadata    captureprojection.StreamMetadata
	err         error
}

func NewScreenedPipe(ctx context.Context, projector *captureprojection.Projector, scope captureprojection.Scope, label string, source io.ReadCloser, options captureprojection.StreamOptions, budget *ByteBudget) (*ScreenedPipe, error) {
	if ctx == nil || projector == nil || source == nil || options.WorkingMemory == nil {
		return nil, errors.New("screened pipe requires context, capture screening, source and memory admission")
	}
	pipe, err := NewBytePipe(ctx, budget)
	if err != nil {
		return nil, err
	}
	screenCtx, cancel := context.WithCancelCause(ctx)
	out := &ScreenedPipe{pipe: pipe, source: source, done: make(chan struct{}), cancel: cancel}
	go func() {
		select {
		case <-screenCtx.Done():
			_ = out.closeSource()
		case <-out.done:
		}
	}()
	go func() {
		defer close(out.done)
		defer out.closeSource()
		metadata, err := projector.ScreenStream(screenCtx, scope, label, source, pipe, options)
		termination := StreamScreeningFailed
		out.mu.Lock()
		if cause := context.Cause(screenCtx); cause != nil && (err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			err = cause
			metadata.Complete = false
			termination = StreamCanceled
			if errors.Is(cause, ErrConsumerClosed) {
				termination = StreamConsumerClosed
			}
		}
		out.metadata, out.err, out.settled = metadata, err, true
		out.mu.Unlock()
		defer cancel(nil)
		if out.err != nil {
			pipe.finish(termination, out.err)
		} else {
			_ = pipe.CloseProducer(nil)
		}
	}()
	return out, nil
}

func (s *ScreenedPipe) Read(p []byte) (int, error) { return s.pipe.Read(p) }

// Close stops downstream consumption and closes the raw source. Wait settles
// the producing screen separately; downstream closure does not prove its success.
func (s *ScreenedPipe) Close() error {
	var err error
	s.once.Do(func() {
		s.mu.Lock()
		_ = s.pipe.CloseConsumer()
		if !s.settled {
			s.cancel(ErrConsumerClosed)
		}
		s.mu.Unlock()
		err = s.closeSource()
	})
	return err
}

func (s *ScreenedPipe) closeSource() error {
	s.sourceOnce.Do(func() { s.sourceError = s.source.Close() })
	return s.sourceError
}

// Wait returns the capture owner's final hashes and revision facts separately
// from all transferred bytes. It does not authorize effect publication.
func (s *ScreenedPipe) Wait(ctx context.Context) (captureprojection.StreamMetadata, error) {
	select {
	case <-s.done:
		return s.metadata, s.err
	case <-ctx.Done():
		return captureprojection.StreamMetadata{}, ctx.Err()
	}
}
