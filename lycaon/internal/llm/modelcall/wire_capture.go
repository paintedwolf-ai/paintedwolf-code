package modelcall

import (
	"sync"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// WireCapture retains a single dispatch: the serialized adapter request and the
// provider-neutral request the adapter serialized it from, after every host
// screen ran. Multiple dispatches are ambiguous without per-attempt response
// bindings and discard both.
type WireCapture struct {
	mu     sync.Mutex
	body   []byte
	source *CompletionRequest
	count  int
}

const MaxTrainingRequestBytes = 16 << 20

func (c *WireCapture) record(req *CompletionRequest, body []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	if c.count != 1 || len(body) > MaxTrainingRequestBytes {
		c.body, c.source = nil, nil
		return
	}
	c.body = append([]byte(nil), body...)
	if req != nil {
		source := *req
		source.Messages = append([]api.Message(nil), req.Messages...)
		source.Tools = append([]tools.ToolMeta(nil), req.Tools...)
		source.ControlCapture, source.sourceCapture = nil, nil
		c.source = &source
	}
}

func (c *WireCapture) Record(body []byte) { c.record(nil, body) }

// Snapshot returns the serialized body, the request it came from (nil when the
// adapter recorded no source), and the dispatch count.
func (c *WireCapture) Snapshot() ([]byte, *CompletionRequest, int) {
	if c == nil {
		return nil, nil, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.body...), c.source, c.count
}
