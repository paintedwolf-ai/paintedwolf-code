package actionoutcome

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// A handoff keeps its witness for paths that attach after dispatch.
type Handoff struct {
	mu     sync.Mutex
	result Result
	paths  []*Path
}

func (h *Handoff) Follow(p *Path) {
	if h == nil || p == nil {
		return
	}
	h.mu.Lock()
	result := h.result
	if result.State == "" {
		h.paths = append(h.paths, p)
	}
	h.mu.Unlock()
	if result.State != "" {
		p.Resolve(result)
	}
}

func (h *Handoff) Resolve(result Result) {
	h.mu.Lock()
	if h.result.State != "" {
		h.mu.Unlock()
		return
	}
	h.result = result
	paths := h.paths
	h.paths = nil
	h.mu.Unlock()
	for _, p := range paths {
		p.Resolve(result)
	}
}

type leaseKey struct{}

type leaseWitness struct {
	id       string
	delivery *Handoff
}

func WithLease(ctx context.Context, id string, delivery *Handoff) context.Context {
	return context.WithValue(ctx, leaseKey{}, leaseWitness{id, delivery})
}
func CurrentTurn(ctx context.Context) *Turn { t, _ := ctx.Value(turnContextKey{}).(*Turn); return t }

func (t *Ticket) Consumed(turn *Turn) {
	if t == nil {
		return
	}
	if turn == nil {
		t.Record(api.WakeStateDropped, api.WakeReasonDefect, "", "")
		return
	}
	t.Record(api.WakeStateConsumed, "", turn.id, "")
	turn.handoff.Follow(t.path)
}
func (t *Ticket) Routed(leaseID string, delivery *Handoff) {
	if t == nil {
		return
	}
	t.Record(api.WakeStateRoutedToWait, "", "", leaseID)
	delivery.Follow(t.path)
}
