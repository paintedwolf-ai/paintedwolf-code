package linuxsandbox

import (
	"encoding/json"
	"io"
	"sync"
)

const (
	maxLaunchDistinctRefusals = 1056
	maxLaunchRepeatsPerKey    = 64
)

// refusalLines writes one JSON line per refusal to the action's feed. A lost
// line or cap overflow degrades the record, never the decision.
type refusalLines struct {
	mu       sync.Mutex
	w        io.Writer
	seen     map[string]int
	degraded bool
}

func (r *refusalLines) report(x Refusal) {
	if r == nil || r.w == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !x.Unexamined {
		if r.seen == nil {
			r.seen = map[string]int{}
		}
		key := x.Operation + "\x00" + x.Target
		if count, exists := r.seen[key]; exists {
			if count >= maxLaunchRepeatsPerKey {
				return
			}
			r.seen[key] = count + 1
		} else if len(r.seen) >= maxLaunchDistinctRefusals {
			if !r.degraded {
				r.degraded = true
				_, _ = r.w.Write([]byte("{\"unexamined\":true}\n"))
			}
			return
		} else {
			r.seen[key] = 1
		}
	}
	line, err := json.Marshal(x)
	if err != nil {
		return
	}
	_, _ = r.w.Write(append(line, '\n'))
}
