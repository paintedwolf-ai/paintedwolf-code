package transcript

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/lycaon/lycaon/internal/observability"
	"github.com/lycaon/lycaon/pkg/api"
)

var secretSweepLog = slog.With("component", "secret_sweep")

// sweepQueue serializes sweeps per session tree and collapses a burst into one
// follow-up pass. Reading a credential file harvests many values at once, and a
// sweep per value would have several passes rewriting the same rows at once.
type sweepQueue struct {
	mu      sync.Mutex
	active  map[string]bool
	pending map[string]uint64
	// base is the stamp floor per tree, read from the store once per process.
	base map[string]uint64
}

// claim reports whether the caller should run the sweep now. When one is
// already running for this tree, the request is folded into its follow-up.
func (q *sweepQueue) claim(root string, generation uint64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.active == nil {
		q.active = map[string]bool{}
		q.pending = map[string]uint64{}
	}
	if q.active[root] {
		if generation > q.pending[root] {
			q.pending[root] = generation
		}
		return false
	}
	q.active[root] = true
	return true
}

// next returns the generation a follow-up pass must cover, if any.
func (q *sweepQueue) next(root string) (uint64, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if generation, ok := q.pending[root]; ok {
		delete(q.pending, root)
		return generation, true
	}
	delete(q.active, root)
	return 0, false
}

// baseFor returns the tree's stamp floor and whether it has been read yet.
func (q *sweepQueue) baseFor(root string) (uint64, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	base, ok := q.base[root]
	return base, ok
}

// raiseBase records the highest stamp floor.
func (q *sweepQueue) raiseBase(root string, base uint64) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.base == nil {
		q.base = map[string]uint64{}
	}
	if held, ok := q.base[root]; ok && held > base {
		return held
	}
	q.base[root] = base
	return base
}

// SweepSessionTree re-screens transcript rows and their search index.
// Other durable surfaces retain their write-time screening.
func (m *Service) SweepSessionTree(ctx context.Context, rootSessionID string, evidenceRevision uint64) {
	if m == nil || m.store == nil || m.redactMessageForStorage == nil || rootSessionID == "" {
		return
	}
	if !m.secretSweeps.claim(rootSessionID, evidenceRevision) {
		return
	}
	for {
		m.sweepSessionTreeOnce(ctx, rootSessionID, evidenceRevision)
		next, more := m.secretSweeps.next(rootSessionID)
		if !more {
			return
		}
		evidenceRevision = next
	}
}

// screenGeneration places local revisions above the persisted tree floor.
func (m *Service) screenGeneration(ctx context.Context, rootSessionID string, evidenceRevision uint64) (uint64, error) {
	base, ok := m.secretSweeps.baseFor(rootSessionID)
	if !ok {
		stamped, err := m.store.MaxMessageScreenGenerationInTree(ctx, rootSessionID)
		if err != nil {
			return 0, err
		}
		base = m.secretSweeps.raiseBase(rootSessionID, stamped)
	}
	return base + evidenceRevision, nil
}

func (m *Service) sweepSessionTreeOnce(ctx context.Context, rootSessionID string, evidenceRevision uint64) {
	generation, err := m.screenGeneration(ctx, rootSessionID, evidenceRevision)
	if err != nil {
		secretSweepLog.WarnContext(ctx, "secret sweep could not read the screen generation floor",
			"root_session_id", observability.ShortSessionID(rootSessionID), "error", err)
		return
	}
	sessionIDs, err := m.store.SessionTreeIDs(ctx, rootSessionID)
	if err != nil {
		secretSweepLog.WarnContext(ctx, "secret sweep could not resolve the session tree",
			"root_session_id", observability.ShortSessionID(rootSessionID), "error", err)
		return
	}
	rewritten := 0
	for _, sessionID := range sessionIDs {
		n, err := m.sweepSession(ctx, sessionID, generation)
		// Count successful rewrites even when a session reports an error.
		rewritten += n
		if err != nil {
			secretSweepLog.WarnContext(ctx, "secret sweep left rows unscreened in a session",
				"session_id", observability.ShortSessionID(sessionID), "error", err)
		}
	}
	if rewritten > 0 {
		secretSweepLog.InfoContext(ctx, "secret sweep rewrote stored rows",
			"root_session_id", observability.ShortSessionID(rootSessionID),
			"generation", generation, "rows", rewritten)
	}
}

// sweepSession rescreens one session's stale rows, returning the rewrite count.
func (m *Service) sweepSession(ctx context.Context, sessionID string, generation uint64) (int, error) {
	stale, err := m.store.MessageIDsBelowScreenGeneration(ctx, sessionID, generation)
	if err != nil || len(stale) == 0 {
		return 0, err
	}
	staleIDs := make(map[string]struct{}, len(stale))
	for _, id := range stale {
		staleIDs[id] = struct{}{}
	}
	messages, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	rewritten := 0
	var failed error
	for _, msg := range messages {
		if _, ok := staleIDs[msg.ID]; !ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			return rewritten, errors.Join(failed, err)
		}
		changed, err := m.rescreenStoredMessage(ctx, sessionID, msg, generation)
		if changed {
			rewritten++
		}
		if err != nil {
			// Failed rows stay unstamped while the pass continues.
			failed = errors.Join(failed, fmt.Errorf("message %s: %w", msg.ID, err))
		}
	}
	return rewritten, failed
}

// rescreenStoredMessage rewrites a row when current evidence changes it.
func (m *Service) rescreenStoredMessage(ctx context.Context, sessionID string, msg api.Message, generation uint64) (bool, error) {
	screened, changed := m.redactMessageForStorage(m.SecretContext(ctx, sessionID), msg)
	if !changed {
		return false, m.store.StampMessageScreenGeneration(ctx, sessionID, msg.ID, generation)
	}
	// Seq advances so a client holding the earlier row accepts the rewrite; ord
	// and ts are creation-time.
	screened.Seq = msg.Seq + 1
	if _, err := m.store.UpdateMessage(ctx, sessionID, msg.ID, screened); err != nil {
		return false, err
	}
	if err := m.store.StampMessageScreenGeneration(ctx, sessionID, msg.ID, generation); err != nil {
		return true, err
	}
	// Observers render from the row, so a silent rewrite would leave a value on
	// screen that the store no longer holds.
	m.PublishPatch(ctx, sessionID, screened)
	return true, nil
}
