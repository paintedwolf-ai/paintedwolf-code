package contract

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// outboxFixture is a module shaped like the host's persistence: sqlc
// queries, an outbox with EnqueueTx, stores that write through both, and
// callers reaching them through interfaces.
var outboxFixture = map[string]string{
	"go.mod": "module example.com/outboxfixture\n\ngo 1.24\n",
	"internal/db/queries/widgets.sql": `-- name: InsertWidget :exec
-- Callers that insert into audits do so in their own query.
INSERT INTO widgets (id) VALUES (?);

-- name: UpdateWidget :exec
UPDATE widgets SET name = ? WHERE id = ?;

-- name: InsertAudit :exec
INSERT INTO audits (id) VALUES (?);
`,
	"internal/db/db.go": `package db

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
	"internal/eventoutbox/outbox.go": `package eventoutbox

import (
	"context"
	"database/sql"
)

type Outbox struct{}

func (o *Outbox) EnqueueTx(ctx context.Context, tx *sql.Tx, topic string) error { return nil }
`,
	"internal/store/store.go": `package store

import (
	"context"
	"database/sql"

	"example.com/outboxfixture/internal/db"
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
	"internal/service/service.go": `package service

import (
	"context"
	"database/sql"

	"example.com/outboxfixture/internal/store"
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
	"internal/cache/cache.go": `package cache

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

func TestOutboxScanResolvesCallsByType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for rel, body := range outboxFixture {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		contractcheck.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(path), 0o755))
		contractcheck.FailErr(t, "write "+rel, os.WriteFile(path, []byte(body), 0o600))
	}
	const module = "example.com/outboxfixture"
	tree := outboxTree{
		moduleDir:  dir,
		keyPrefix:  "fixture/",
		dbPackage:  module + "/internal/db",
		queriesDir: filepath.Join(dir, "internal", "db", "queries"),
		enqueue:    map[string]bool{"(*" + module + "/internal/eventoutbox.Outbox).EnqueueTx": true},
		env:        []string{"GOFLAGS=-mod=mod", "GOWORK=off"},
	}
	scan, err := tree.scan(nil)
	contractcheck.FailErr(t, "scan the fixture module", err)

	silent := scan.silentMutators()
	keys := make([]string, 0, len(silent))
	for key := range silent {
		keys = append(keys, key)
	}
	// Unannounced writes to an event-carrying table are reported: one with no
	// announcing caller, one whose caller's only announce happens in another
	// package's transaction, and a helper below an announcing caller that is
	// not handed its transaction. Helpers that write inside an announcing
	// transaction, the look-alike names in cache, and the audits write (no
	// announcing writer makes audits an event table) are not.
	contractcheck.FailSetEqual(t, "silent mutators", []string{
		"fixture/internal/store/store.go:MarkSeen",
		"fixture/internal/store/store.go:RenameWidget",
		"fixture/internal/store/store.go:auditWidget",
	}, keys)
	if got := silent["fixture/internal/store/store.go:RenameWidget"]; !slices.Equal(got, []string{"widgets"}) {
		t.Fatalf("RenameWidget tables = %v, want [widgets]", got)
	}
	// InsertWidget's comment names audits in prose; comments write nothing.
	if scan.eventTables["audits"] {
		t.Fatal("audits became an event table without an announcing writer")
	}
	announces := map[string]bool{}
	for _, fn := range scan.funcs {
		announces[fn.key] = scan.announces[fn]
	}
	for key, want := range map[string]bool{
		"fixture/internal/store/store.go:CreateWidget":        true,
		"fixture/internal/service/service.go:Rebuild":         true, // interface dispatch to *store.Store
		"fixture/internal/cache/cache.go:Refresh":             false,
		"fixture/internal/cache/cache.go:Warm":                false,
		"fixture/internal/service/service.go:Touch":           false, // reaches an enqueue in another transaction
		"fixture/internal/store/store.go:CreateStampedWidget": true,
	} {
		if announces[key] != want {
			t.Errorf("%s announces = %v, want %v", key, announces[key], want)
		}
	}
	if len(scan.enqueueSites) != 2 { // Log reaches CreateWidget's enqueue
		t.Fatalf("enqueue sites = %+v, want the two store enqueues", scan.enqueueSites)
	}
}
