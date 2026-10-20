package tooloutput

import (
	"context"
	"fmt"
	"io"
)

func (s *StreamSpill) RetentionClaimID() string  { return s.reference.RetentionClaimID }
func (s *StreamSpill) InitiatingClaimID() string { return s.reference.InitiatingClaimID }

func (s *StreamSpill) Open(ctx context.Context) (io.ReadCloser, error) {
	s.mu.Lock()
	if s.closed || s.body == nil {
		s.mu.Unlock()
		return nil, fmt.Errorf("stream wrapper no longer owns its body")
	}
	body := s.body
	s.opening++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.opening--; s.mu.Unlock() }()
	return body.Open(ctx)
}

func (s *StreamSpill) releaseDisplay() {
	s.Preview = nil
	if s.releasePreview != nil {
		s.releasePreview()
		s.releasePreview = nil
	}
}

// Close closes this display and its untransferred body. A body whose collection
// is blocked retains its leases; retry Close after active/recovery refs settle.
func (s *StreamSpill) Close() error {
	s.mu.Lock()
	s.closed = true
	s.releaseDisplay()
	body := s.body
	s.mu.Unlock()
	if body == nil {
		return nil
	}
	return body.Close()
}

// TransferBody commits attachment before moving the SAME claim and body leases.
// persist must durably store the exact reference before returning nil and must
// not reenter this wrapper. On failure ownership remains here. Once persist
// succeeds, cancellation cannot undo attachment; transfer completes regardless.
// The receiver separately admits its durable record and reconstructs boot refs.
func (s *StreamSpill) TransferBody(ctx context.Context, persist func(context.Context, RetainedStreamReference) error) (*RetainedStreamBody, error) {
	if ctx == nil || persist == nil {
		return nil, fmt.Errorf("body transfer requires context and durable attachment")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.closed || s.body == nil || s.opening != 0 {
		return nil, fmt.Errorf("stream wrapper cannot transfer body")
	}
	body := s.body
	body.mu.Lock()
	busy := body.active != 0 || body.closed || body.closing
	body.mu.Unlock()
	if busy {
		return nil, fmt.Errorf("stream body still has active readers or is closing")
	}
	if err := persist(ctx, s.reference); err != nil {
		return nil, err
	}
	s.body = nil
	s.closed = true
	s.releaseDisplay()
	return body, nil
}
