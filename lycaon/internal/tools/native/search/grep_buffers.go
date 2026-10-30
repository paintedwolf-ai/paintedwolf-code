package search

import "context"

// grepBuffers transfers fixed storage from workers to the ordered reducer.
type grepBuffers struct{ available chan []byte }

func newGrepBuffers(slots int) *grepBuffers {
	buffers := &grepBuffers{available: make(chan []byte, slots)}
	for range slots {
		buffers.available <- make([]byte, 0, grepOutputChunk)
	}
	return buffers
}

func (b *grepBuffers) get(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b == nil {
		return make([]byte, 0, grepOutputChunk), ctx.Err()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case buffer := <-b.available:
		return buffer, nil
	}
}

func (b *grepBuffers) put(buffer []byte) {
	if b == nil || buffer == nil {
		return
	}
	b.available <- buffer[:0]
}

func (b *grepBuffers) clear() { b.available = nil }
