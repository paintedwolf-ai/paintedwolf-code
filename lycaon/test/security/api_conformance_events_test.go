package security

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// conformanceHostIDs answers whether an id names a registered project or chat.
type conformanceHostIDs struct {
	project func(ctx context.Context, id string) (bool, error)
	session func(ctx context.Context, id string) (bool, error)
}

// checkEventScopes: every event scoped to a project carries a UUID naming a
// registered project, and every chat-scoped event also names a real chat.
func (s *conformanceSweep) checkEventScopes(published []wire.EventEnvelope, ids conformanceHostIDs) {
	ctx := context.Background()
	for _, env := range published {
		topic, scope := string(env.Topic), env.Scope
		switch scope.Kind {
		case wire.EventScopeProject, wire.EventScopeSession:
			s.checkScopeID(ctx, topic, "project_id", scope.ProjectID, ids.project)
		case wire.EventScopeDevice:
			if scope.ProjectID != "" || scope.SessionID != "" {
				s.findings.add(ruleEventScopesAreHostIDs, topic, "device scope", "carries project %q session %q", scope.ProjectID, scope.SessionID)
			}
		default:
			s.findings.add(ruleEventScopesAreHostIDs, topic, "scope", "unknown scope kind %q", scope.Kind)
		}
		if scope.Kind == wire.EventScopeSession {
			s.checkScopeID(ctx, topic, "session_id", scope.SessionID, ids.session)
		}
	}
}

func (s *conformanceSweep) checkScopeID(ctx context.Context, topic, field, id string, exists func(context.Context, string) (bool, error)) {
	if _, err := uuid.Parse(id); err != nil {
		s.findings.add(ruleEventScopesAreHostIDs, topic, "scope "+field, "%q is not a UUID", id)
		return
	}
	ok, err := exists(ctx, id)
	switch {
	case err != nil:
		s.findings.add(ruleEventScopesAreHostIDs, topic, "scope "+field, "could not look up %s: %v", id, err)
	case !ok:
		s.findings.add(ruleEventScopesAreHostIDs, topic, "scope "+field, "%s names nothing the host registered", id)
	}
}

// conformanceHostLookup reads the durable store the server writes.
func conformanceHostLookup(t *testing.T, h *wiring.Harness) conformanceHostIDs {
	t.Helper()
	exists := func(query string) func(context.Context, string) (bool, error) {
		return func(ctx context.Context, id string) (bool, error) {
			var found bool
			err := h.DB.QueryRowContext(ctx, query, id).Scan(&found)
			return found, err
		}
	}
	return conformanceHostIDs{
		project: exists(`SELECT EXISTS (SELECT 1 FROM projects WHERE id = ?)`),
		session: exists(`SELECT EXISTS (SELECT 1 FROM sessions WHERE id = ?)`),
	}
}

// conformanceEventCapture records every event the hub delivers while the
// sweep runs.
type conformanceEventCapture struct {
	unsubscribe func()
	done        chan struct{}
	mu          sync.Mutex
	events      []wire.EventEnvelope
}

func startConformanceEventCapture(t *testing.T, hub *events.MemoryHub) *conformanceEventCapture {
	t.Helper()
	ch, unsubscribe, err := hub.Subscribe(t.Context(), events.Subscription{Viewer: testutil.HostOwner()})
	testutil.FailErr(t, "subscribe to every event", err)
	c := &conformanceEventCapture{unsubscribe: unsubscribe, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		for env := range ch {
			c.mu.Lock()
			c.events = append(c.events, env)
			c.mu.Unlock()
		}
	}()
	t.Cleanup(unsubscribe)
	return c
}

// stop ends the capture and returns every event. The hub closes a subscriber
// whose buffer overflows; a capture that ended early would silently miss
// events, so it fails the sweep.
func (c *conformanceEventCapture) stop(t *testing.T) []wire.EventEnvelope {
	t.Helper()
	select {
	case <-c.done:
		t.Fatal("the event capture fell behind and the hub dropped it")
	default:
	}
	c.unsubscribe()
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]wire.EventEnvelope(nil), c.events...)
}
