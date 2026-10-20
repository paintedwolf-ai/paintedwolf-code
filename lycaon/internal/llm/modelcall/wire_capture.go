package modelcall

import "sync"

// WireCapture retains a single serialized request. Multiple dispatches are
// ambiguous without per-attempt response bindings and discard the payload.
type WireCapture struct {
	mu    sync.Mutex
	body  []byte
	count int
}

const MaxTrainingRequestBytes = 16 << 20

func (c *WireCapture) Record(body []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	if c.count != 1 || len(body) > MaxTrainingRequestBytes {
		c.body = nil
		return
	}
	c.body = append([]byte(nil), body...)
}

func (c *WireCapture) Snapshot() ([]byte, int) {
	if c == nil {
		return nil, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.body...), c.count
}
