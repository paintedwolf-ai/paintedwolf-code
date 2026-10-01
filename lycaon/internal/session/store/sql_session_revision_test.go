package store

import (
	"github.com/lycaon/lycaon/internal/eventoutbox"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

// Reading the outbox directly avoids delivery timing in the revision assertion.
func TestSQLSessionEventSharesRevisionWithDirectPublishPath(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())

	hub := events.NewMemoryHub()
	outbox := eventoutbox.New(database, hub) // not started: the row stays put for inspection
	store := NewSQL(database)
	store.SetEventOutbox(outbox)

	pub := &events.Publisher{Hub: hub}
	store.SetSessionRevisions(pub)

	if got := pub.NextSessionRevision(); got != 1 {
		t.Fatalf("direct path's first revision = %d, want 1", got)
	}

	sess, err := store.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	var outboxRevision uint64
	err = database.QueryRowContext(t.Context(),
		`SELECT entity_revision FROM event_outbox WHERE topic = ? AND session_id = ? ORDER BY id DESC LIMIT 1`,
		string(api.EventTopicSession), sess.ID,
	).Scan(&outboxRevision)
	testutil.FailErr(t, "read outbox row revision", err)
	if outboxRevision != 2 {
		t.Fatalf("outbox-enqueued session event revision = %d, want 2 (the shared counter's second mint, after the direct path already consumed the first)", outboxRevision)
	}

	if got := pub.NextSessionRevision(); got != 3 {
		t.Fatalf("direct path's revision after the outbox enqueue = %d, want 3 (must continue the shared sequence, not run a separate one)", got)
	}
}

// An unwired store enqueues with no EntityRevision rather than panicking.
func TestSQLSessionEventUnwiredRevisionsLeavesZero(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, database, testdbseed.DefaultProjectID, t.TempDir())

	hub := events.NewMemoryHub()
	outbox := eventoutbox.New(database, hub)
	store := NewSQL(database)
	store.SetEventOutbox(outbox)

	sess, err := store.Create(t.Context(), api.CreateSessionRequest{Posture: api.SessionPostureBuild}, testdbseed.DefaultProjectID)
	testutil.FailErr(t, "create session", err)

	var outboxRevision uint64
	err = database.QueryRowContext(t.Context(),
		`SELECT entity_revision FROM event_outbox WHERE topic = ? AND session_id = ? ORDER BY id DESC LIMIT 1`,
		string(api.EventTopicSession), sess.ID,
	).Scan(&outboxRevision)
	testutil.FailErr(t, "read outbox row revision", err)
	if outboxRevision != 0 {
		t.Fatalf("outbox-enqueued session event revision = %d, want 0 (unwired store must not fabricate a revision)", outboxRevision)
	}
}
