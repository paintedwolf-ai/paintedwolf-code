package projectpaths

import (
	"context"
	"errors"
	"io"
	"sync"
)

// OpenNullOutput exposes only the already bound null operand as a discard sink.
// Ordinary entries still require their native mutation operation and admission.
func (p BoundPath) OpenNullOutput(ctx context.Context) (io.WriteCloser, error) {
	if ctx == nil || !p.Null {
		return nil, &PathError{Code: "COMMAND_OPERAND_INVALID", Cause: errors.New("null output requires context and a typed null operand")}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &nullOutput{ctx: ctx}, nil
}

type nullOutput struct {
	ctx    context.Context
	mu     sync.Mutex
	closed bool
}

func (s *nullOutput) Write(bytes []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}
	if err := s.ctx.Err(); err != nil {
		return 0, err
	}
	return len(bytes), nil
}
func (s *nullOutput) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
