package confine

import (
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

// retainedOrphanRefusals bounds the refusals remembered so a retrying process
// is warned about once per destination.
const retainedOrphanRefusals = 128

var orphanEgress = struct {
	mu    sync.Mutex
	seen  map[string]bool
	order []string
}{seen: map[string]bool{}}

// reportRefusal warns once per destination about a caller turned away before
// any destination decision, so a retrying process cannot flood the log.
func (b *egressBrokerT) reportRefusal(peer egressproxy.Peer, ep egressproxy.Endpoint, refusal egressproxy.Refusal) {
	key := peer.Lineage + "\x00" + string(refusal) + "\x00" + ep.Host + "\x00" + string(ep.Transport)
	orphanEgress.mu.Lock()
	known := orphanEgress.seen[key]
	if !known {
		if len(orphanEgress.order) >= retainedOrphanRefusals {
			delete(orphanEgress.seen, orphanEgress.order[0])
			orphanEgress.order = orphanEgress.order[1:]
		}
		orphanEgress.seen[key] = true
		orphanEgress.order = append(orphanEgress.order, key)
	}
	orphanEgress.mu.Unlock()
	if known {
		return
	}
	slog.Warn("mediated egress refused an unleased process",
		"component", "sandbox",
		"reason", string(refusal),
		"owner", peer.Owner,
		"destination", ep.Host,
		"port", ep.Port,
		"transport", string(ep.Transport),
	)
}
