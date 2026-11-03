package commandprogram

import (
	"context"
	"errors"
	"io"
	"sync"
)

// StreamInput meters complete input bytes before they reach a stage. The source
// must unblock a pending read when closed; cancellation closes that source.
type StreamInput struct {
	ctx           context.Context
	source        io.ReadCloser
	budget        *ByteBudget
	mu            sync.Mutex
	termination   StreamTermination
	done          chan struct{}
	once          sync.Once
	terminalError error
}

func NewStreamInput(ctx context.Context, source io.ReadCloser, budget *ByteBudget) (*StreamInput, error) {
	if ctx == nil || source == nil || budget == nil {
		return nil, errors.New("stream input requires context, source and ingress budget")
	}
	s := &StreamInput{ctx: ctx, source: source, budget: budget, termination: StreamOpen, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			s.finish(StreamCanceled, ctx.Err())
		case <-s.done:
		}
	}()
	return s, nil
}

func (s *StreamInput) Read(b []byte) (int, error)                             { return s.read(s.ctx, b) }
func (s *StreamInput) ReadContext(ctx context.Context, b []byte) (int, error) { return s.read(ctx, b) }
func (s *StreamInput) InputReady() bool {
	s.mu.Lock()
	ready := s.termination != StreamOpen
	s.mu.Unlock()
	return ready || inputReady(s.source)
}

func (s *StreamInput) read(ctx context.Context, b []byte) (int, error) {
	s.mu.Lock()
	termination, terminalError := s.termination, s.terminalError
	s.mu.Unlock()
	if termination != StreamOpen {
		if terminalError == nil {
			terminalError = io.EOF
		}
		return 0, terminalError
	}
	if err := s.ctx.Err(); err != nil {
		s.finish(StreamCanceled, err)
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n, err := readContext(ctx, s.source, b)
	if canceled := s.ctx.Err(); canceled != nil {
		s.finish(StreamCanceled, canceled)
		return 0, canceled
	}
	if charge := s.budget.Charge(int64(n)); charge != nil {
		s.finish(StreamBudgetExceeded, charge)
		return 0, charge
	}
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	if errors.Is(err, io.EOF) {
		s.finish(StreamEOF, nil)
	} else if err != nil {
		s.finish(StreamIncomplete, err)
	}
	return n, err
}

func (s *StreamInput) finish(termination StreamTermination, err error) {
	s.once.Do(func() {
		s.mu.Lock()
		s.termination = termination
		s.terminalError = err
		s.mu.Unlock()
		close(s.done)
		_ = s.source.Close()
	})
}

func (s *StreamInput) Termination() StreamTermination {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.termination
}
func (s *StreamInput) Close() error { s.finish(StreamConsumerClosed, ErrConsumerClosed); return nil }

// NullInput and NullOutput are typed native endpoints. Path classification is
// supplied by the path adapter; these endpoints never open an OS device path.
func NullInput() io.ReadCloser { return nullInput{} }

// IsNullInput reports whether r is the command program's null input endpoint.
func IsNullInput(r io.Reader) bool {
	switch in := r.(type) {
	case nullInput:
		return true
	case *StreamInput:
		return IsNullInput(in.source)
	default:
		return false
	}
}

type nullInput struct{}

func (nullInput) InputReady() bool { return true }

func (nullInput) Read([]byte) (int, error) { return 0, io.EOF }
func (nullInput) Close() error             { return nil }

type NullOutput struct {
	Context context.Context
	Budget  *ByteBudget
}

func (s NullOutput) Write(b []byte) (int, error) {
	if s.Context == nil || s.Budget == nil {
		return 0, errors.New("null output requires context and ingress budget")
	}
	if err := s.Context.Err(); err != nil {
		return 0, err
	}
	if err := s.Budget.Charge(int64(len(b))); err != nil {
		return 0, err
	}
	return len(b), nil
}

// borrowedInput leaves the descriptor lifetime with its enclosing scope.
type borrowedInput struct{ io.Reader }

func (b borrowedInput) Close() error { return nil }
func (b borrowedInput) ReadContext(ctx context.Context, data []byte) (int, error) {
	return readContext(ctx, b.Reader, data)
}
func (b borrowedInput) InputReady() bool { return inputReady(b.Reader) }
