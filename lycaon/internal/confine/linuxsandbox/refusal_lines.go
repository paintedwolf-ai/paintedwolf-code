package linuxsandbox

import (
	"encoding/json"
	"io"
	"sync"
)

// refusalLines writes one JSON line per refusal to the action's feed. A lost
// line degrades the record, never the decision.
type refusalLines struct {
	mu sync.Mutex
	// w is nil when the action has no refusal feed.
	w io.Writer
}

func (r *refusalLines) report(x Refusal) {
	if r == nil || r.w == nil {
		return
	}
	line, err := json.Marshal(x)
	if err != nil {
		return
	}
	r.mu.Lock()
	_, _ = r.w.Write(append(line, '\n'))
	r.mu.Unlock()
}
