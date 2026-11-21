package invariants_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/contract/invariants"
	"github.com/lycaon/lycaon/test/contract/invariants/outbox"
)

// outboxFixture is a module shaped like the host's persistence: sqlc
// queries, an outbox with EnqueueTx, stores that write through both, and
// callers reaching them through interfaces.
var outboxFixture = map[string]string{
	"lycaon/internal/db/queries/widgets.sql": `-- name: InsertWidget :exec
-- Callers that insert into audits do so in their own query.
INSERT INTO widgets (id) VALUES (?);

-- name: UpdateWidget :exec
UPDATE widgets SET name = ? WHERE id = ?;

-- name: InsertAudit :exec
INSERT INTO audits (id) VALUES (?);
`,
	"lycaon/internal/db/db.go": `package db

import (
	"context"
	"database/sql"
)

type Queries struct{ tx *sql.Tx }

func New(tx *sql.Tx) *Queries { return &Queries{tx: tx} }

func (q *Queries) InsertWidget(ctx context.Context) error { return nil }
func (q *Queries) UpdateWidget(ctx context.Context) error { return nil }
func (q *Queries) InsertAudit(ctx context.Context) error  { return nil }
`,
	"lycaon/internal/eventoutbox/outbox.go": `package eventoutbox

import (
	"context"
	"database/sql"
)

type Outbox struct{}

func (o *Outbox) EnqueueTx(ctx context.Context, tx *sql.Tx, topic string) error { return nil }
`,
	"lycaon/internal/store/store.go": `package store

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db"
)

type enqueuer interface {
	EnqueueTx(ctx context.Context, tx *sql.Tx, topic string) error
}

type Store struct{ out enqueuer }

// CreateWidget announces its write: widgets carries events.
func (s *Store) CreateWidget(ctx context.Context, tx *sql.Tx) error {
	if err := db.New(tx).InsertWidget(ctx); err != nil {
		return err
	}
	if err := s.recordCreation(ctx); err != nil {
		return err
	}
	return s.out.EnqueueTx(ctx, tx, "widget")
}

// recordCreation is covered by its announcing caller; the helper it calls is
// not handed the transaction, so its write stands alone.
func (s *Store) recordCreation(ctx context.Context) error {
	return s.auditWidget(ctx)
}

func (s *Store) auditWidget(ctx context.Context) error {
	return db.New(nil).UpdateWidget(ctx)
}

// CreateStampedWidget announces; the write it delegates is covered by it.
func (s *Store) CreateStampedWidget(ctx context.Context, tx *sql.Tx) error {
	if err := s.stampWidget(ctx, tx); err != nil {
		return err
	}
	return s.out.EnqueueTx(ctx, tx, "widget")
}

func (s *Store) stampWidget(ctx context.Context, tx *sql.Tx) error {
	return setWidgetName(ctx, db.New(tx))
}

// setWidgetName writes inside the transaction it is handed.
func setWidgetName(ctx context.Context, q *db.Queries) error {
	return q.UpdateWidget(ctx)
}

// RenameWidget changes an event-carrying row and announces nothing.
func (s *Store) RenameWidget(ctx context.Context, tx *sql.Tx) error {
	return db.New(tx).UpdateWidget(ctx)
}

// Log opens its own transaction and announces.
func (s *Store) Log(ctx context.Context) error {
	return s.CreateWidget(ctx, nil)
}

// MarkSeen writes an event-carrying row outside any announcing transaction.
func (s *Store) MarkSeen(ctx context.Context) error {
	return db.New(nil).UpdateWidget(ctx)
}

// RecordAudit writes a table no announcing function writes.
func (s *Store) RecordAudit(ctx context.Context, tx *sql.Tx) error {
	return db.New(tx).InsertAudit(ctx)
}
`,
	"lycaon/internal/service/service.go": `package service

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/store"
)

// Touch reaches an announcing store method and an unrelated silent one; the
// first announces in its own transaction and does not cover the second.
func Touch(ctx context.Context, st *store.Store) error {
	if err := st.Log(ctx); err != nil {
		return err
	}
	return st.MarkSeen(ctx)
}

// widgets is satisfied by *store.Store.
type widgets interface {
	CreateWidget(ctx context.Context, tx *sql.Tx) error
}

// Rebuild reaches the announcing store method through an interface.
func Rebuild(ctx context.Context, tx *sql.Tx, w widgets) error {
	return w.CreateWidget(ctx, tx)
}
`,
	"lycaon/internal/cache/cache.go": `package cache

// Cache has a method named like a mutating query; it writes nothing.
type Cache struct{ n int }

func (c *Cache) UpdateWidget() { c.n++ }

// Warm calls it; a name-matching scan would call this a widgets writer.
func Warm(c *Cache) { c.UpdateWidget() }

// RenameWidget shares a silent mutator's name and is unrelated to it.
func RenameWidget() {}

// CreateWidget shares an announcing method's name and announces nothing.
func CreateWidget() {}

// Refresh calls the look-alike, not the store's announcing method.
func Refresh() { CreateWidget(); RenameWidget() }
`,
}

// The scan resolves calls by type: unannounced writes to an event-carrying
// table are reported (one with no announcing caller, one whose caller's only
// announce runs in another package's transaction, and a helper below an
// announcing caller that is not handed its transaction). Helpers inside an
// announcing transaction, look-alike names, interface dispatch to an
// announcing store, and a table no announcing writer makes event-carrying
// are not.
func TestOutboxScanResolvesCallsByType(t *testing.T) {
	t.Parallel()
	corpus := fixture(t, outboxFixture)
	res := invariants.Evaluate(corpus, bare(rule(t, outbox.Family, "mutators-announce-their-transition")))
	expect(t, res, violates, 3)
	for i, key := range []string{
		"internal/store/store.go:MarkSeen",
		"internal/store/store.go:RenameWidget",
		"internal/store/store.go:auditWidget",
	} {
		if !strings.HasPrefix(res.Violations[i], key+" writes widgets ") {
			t.Errorf("violation %d = %q, want %s writing widgets", i, res.Violations[i], key)
		}
	}
	expect(t, invariants.Evaluate(corpus, rule(t, outbox.Family, "enqueues-take-a-mutation-transaction")), passes, 0)
}

// The family fails on each kind of drift.
func TestOutboxClosureFailsClosed(t *testing.T) {
	t.Parallel()
	mutators := bare(rule(t, outbox.Family, "mutators-announce-their-transition"))
	exempted := mutators
	exempted.Exempt = []invariants.Exemption{{Key: "internal/store/store.go:CreateWidget", Reason: "fixture"}}
	for name, tc := range map[string]struct {
		rule  invariants.Invariant
		files map[string]string
		want  outcome
	}{
		"seeded violation": {rule: mutators, files: outboxFixture, want: violates},
		"renamed anchor": {rule: mutators, files: with(outboxFixture, map[string]string{
			"lycaon/internal/eventoutbox/outbox.go": strings.ReplaceAll(outboxFixture["lycaon/internal/eventoutbox/outbox.go"], "EnqueueTx", "Enqueue"),
		}), want: missesAnchor},
		"stale exemption": {rule: exempted, files: outboxFixture, want: staleExemption},
		"empty corpus":    {rule: mutators, files: with(outboxFixture, map[string]string{"lycaon/internal/db/queries/widgets.sql": ""}), want: fails},
		"unparsable file": {rule: mutators, files: with(outboxFixture, map[string]string{
			"lycaon/internal/store/broken.go": "package store\n\nfunc broken( {\n",
		}), want: fails},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			expect(t, invariants.Evaluate(fixture(t, tc.files), tc.rule), tc.want, 0)
		})
	}
}
