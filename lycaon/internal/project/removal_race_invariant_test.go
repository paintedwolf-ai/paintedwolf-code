package project

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordingProjectOutbox struct {
	events   []api.ProjectEvent
	notified int
}

func (o *recordingProjectOutbox) EnqueueProjectTx(_ context.Context, _ *sql.Tx, _ string, payload any) error {
	if ev, ok := payload.(api.ProjectEvent); ok {
		o.events = append(o.events, ev)
	}
	return nil
}

func (o *recordingProjectOutbox) Notify() {
	o.notified++
}

func TestSQLRegistryMidRemovalOmitsDeletingProjectAndMapsGetToNotFound(t *testing.T) {
	store := testdbfixture.Open(t, "store.db")
	reg := NewSQLRegistry(store)
	outbox := &recordingProjectOutbox{}
	reg.SetEventOutbox(outbox)

	healthy, err := reg.Create(t.Context(), CreateParams{
		Name:  "Healthy",
		Roots: []AttachRootParams{{Path: t.TempDir(), Label: "healthy-root"}},
	})
	testutil.FailErr(t, "create healthy project", err)

	removing, err := reg.Create(t.Context(), CreateParams{
		Name:  "Removing",
		Roots: []AttachRootParams{{Path: t.TempDir(), Label: "removing-root"}},
	})
	testutil.FailErr(t, "create removing project", err)

	// Deterministically place `removing` in the mid-Delete window where child
	// rows (including project_roots / outside namespace) are gone while the
	// projects row still exists.
	testutil.FailErr(t, "delete child project rows", db.DeleteProjectRows(t.Context(), store, removing.ID))

	listed, err := reg.List(t.Context())
	testutil.FailErr(t, "List during mid-removal", err)
	if len(listed) != 1 || listed[0].ID != healthy.ID {
		t.Fatalf("List = %+v, want only healthy project %s", listed, healthy.ID)
	}

	_, err = reg.Get(t.Context(), removing.ID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(removing) = %v, want ErrNotFound", err)
	}

	// Removal-specific recovery lookup still resolves the interrupted project
	// with empty roots so retry can finish deletion without touching deleted roots.
	forRemoval, err := GetForRemoval(t.Context(), reg, removing.ID)
	testutil.FailErr(t, "GetForRemoval(removing)", err)
	if forRemoval.ID != removing.ID || len(forRemoval.Roots) != 0 || forRemoval.Outside.ID != "" {
		t.Fatalf("GetForRemoval(removing) = %+v", forRemoval)
	}

	gotHealthy, err := reg.Get(t.Context(), healthy.ID)
	testutil.FailErr(t, "Get(healthy)", err)
	if gotHealthy.ID != healthy.ID || gotHealthy.Outside.ID == "" {
		t.Fatalf("Get(healthy) = %+v", gotHealthy)
	}

	// Retrying ordinary Delete on the abandoned partial-deletion state completes
	// and signals the lifecycle deletion event through the outbox.
	beforeNotify := outbox.notified
	testutil.FailErr(t, "retry Delete on partial removal", reg.Delete(t.Context(), removing.ID))
	if _, err := GetForRemoval(t.Context(), reg, removing.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetForRemoval after retry Delete = %v, want ErrNotFound", err)
	}
	foundDeleted := false
	for _, ev := range outbox.events {
		if ev.ID == removing.ID && ev.Action == api.ProjectEventDeleted {
			foundDeleted = true
		}
	}
	if !foundDeleted || outbox.notified <= beforeNotify {
		t.Fatalf("expected deleted lifecycle event and notify, got events=%+v notified=%d", outbox.events, outbox.notified)
	}

	// Genuine DB errors must still propagate (not be mapped to ErrNotFound).
	testutil.FailErr(t, "close store", store.Close())
	if _, err := reg.List(t.Context()); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("List on closed db = %v, want genuine non-ErrNotFound error", err)
	}
	if _, err := reg.Get(t.Context(), healthy.ID); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on closed db = %v, want genuine non-ErrNotFound error", err)
	}
}

func TestSQLRegistryListUsesReaderLaneWhileWriterTransactionHeld(t *testing.T) {
	store := testdbfixture.Open(t, "store.db")
	reg := NewSQLRegistry(store)

	p, err := reg.Create(t.Context(), CreateParams{
		Name:  "ReaderLane",
		Roots: []AttachRootParams{{Path: t.TempDir(), Label: "reader-root"}},
	})
	testutil.FailErr(t, "create project", err)

	// Hold the single durable writer transaction open with an uncommitted write.
	wtx, err := store.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin writer tx", err)
	defer func() { _ = wtx.Rollback() }()
	_, err = wtx.ExecContext(t.Context(), `UPDATE projects SET starred = 1 WHERE id = ?`, p.ID)
	testutil.FailErr(t, "uncommitted update on writer tx", err)

	// List must complete on the concurrent reader lane without blocking on wtx
	// and must observe the committed snapshot (starred = false).
	listed, err := reg.List(t.Context())
	testutil.FailErr(t, "List while writer tx held", err)
	if len(listed) != 1 || listed[0].ID != p.ID || listed[0].Starred {
		t.Fatalf("List during held writer tx = %+v, want committed project with Starred=false", listed)
	}

	testutil.FailErr(t, "rollback writer tx", wtx.Rollback())
}
