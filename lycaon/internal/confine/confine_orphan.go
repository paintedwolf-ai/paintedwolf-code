package confine

import (
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/egressproxy"
)

// retainedOrphanAttempts bounds recorded attempts from processes no action
// answers for. The record explains a failure; it is not a log.
const retainedOrphanAttempts = 128

// OrphanEgressAttempt is one outbound connection the broker refused because no
// live action was accountable for the process that made it.
type OrphanEgressAttempt struct {
	// Owner names the action that left the process running, when the host can
	// still resolve it.
	Owner string
	// Refusal is the broker's stable reason code.
	Refusal string
	Host    string
	Port    uint16
	// Transport distinguishes a readable request from an opaque tunnel.
	Transport string
	Attempts  int
	FirstAt   time.Time
	LastAt    time.Time
}

var orphanEgress = struct {
	mu       sync.Mutex
	attempts map[string]*OrphanEgressAttempt
}{attempts: map[string]*OrphanEgressAttempt{}}

// reportRefusal records a caller turned away before any destination decision,
// and warns once per destination so a retrying process cannot flood the log.
func (b *egressBrokerT) reportRefusal(peer egressproxy.Peer, ep egressproxy.Endpoint, refusal egressproxy.Refusal) {
	key := peer.Lineage + "\x00" + string(refusal) + "\x00" + ep.Host + "\x00" + string(ep.Transport)
	now := time.Now()
	orphanEgress.mu.Lock()
	entry, known := orphanEgress.attempts[key]
	if known {
		entry.Attempts++
		entry.LastAt = now
	} else {
		if len(orphanEgress.attempts) >= retainedOrphanAttempts {
			evictOldestOrphanLocked()
		}
		orphanEgress.attempts[key] = &OrphanEgressAttempt{
			Owner:     peer.Owner,
			Refusal:   string(refusal),
			Host:      ep.Host,
			Port:      ep.Port,
			Transport: string(ep.Transport),
			Attempts:  1,
			FirstAt:   now,
			LastAt:    now,
		}
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

func evictOldestOrphanLocked() {
	oldestKey, oldest := "", time.Time{}
	for key, entry := range orphanEgress.attempts {
		if oldest.IsZero() || entry.LastAt.Before(oldest) {
			oldestKey, oldest = key, entry.LastAt
		}
	}
	delete(orphanEgress.attempts, oldestKey)
}

// OrphanEgressAttempts returns the recorded refusals, newest activity first.
func OrphanEgressAttempts() []OrphanEgressAttempt {
	orphanEgress.mu.Lock()
	out := make([]OrphanEgressAttempt, 0, len(orphanEgress.attempts))
	for _, entry := range orphanEgress.attempts {
		out = append(out, *entry)
	}
	orphanEgress.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}

// ForgetOrphanEgressAttempts clears the recorded refusals.
func ForgetOrphanEgressAttempts() {
	orphanEgress.mu.Lock()
	orphanEgress.attempts = map[string]*OrphanEgressAttempt{}
	orphanEgress.mu.Unlock()
}
