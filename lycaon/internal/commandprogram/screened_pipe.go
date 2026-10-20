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
	pipe     *BytePipe
	source   io.ReadCloser
	done     chan struct{}
	once     sync.Once
	metadata captureprojection.StreamMetadata
	err      error
}

func NewScreenedPipe(ctx context.Context, projector *captureprojection.Projector, scope captureprojection.Scope, label string, source io.ReadCloser, options captureprojection.StreamOptions, budget *ByteBudget) (*ScreenedPipe, error) {
	if ctx == nil || projector == nil || source == nil || options.WorkingMemory == nil {
		return nil, errors.New("screened pipe requires context, capture screening, source and memory admission")
	}
	pipe, err := NewBytePipe(ctx, budget)
	if err != nil {
		return nil, err
	}
	out := &ScreenedPipe{pipe: pipe, source: source, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = source.Close()
		case <-out.done:
		}
	}()
	go func() {
		defer close(out.done)
		defer source.Close()
		out.metadata, out.err = projector.ScreenStream(ctx, scope, label, source, pipe, options)
		if out.err != nil {
			pipe.finish(StreamScreeningFailed, out.err)
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
	s.once.Do(func() { _ = s.pipe.CloseConsumer(); err = s.source.Close() })
	return err
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
