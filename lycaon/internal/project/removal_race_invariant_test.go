package project

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSQLRegistryMidRemovalOmitsDeletingProjectAndMapsGetToNotFound(t *testing.T) {
	store := testdbfixture.Open(t, "store.db")
	reg := NewSQLRegistry(store)

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

	gotHealthy, err := reg.Get(t.Context(), healthy.ID)
	testutil.FailErr(t, "Get(healthy)", err)
	if gotHealthy.ID != healthy.ID || gotHealthy.Outside.ID == "" {
		t.Fatalf("Get(healthy) = %+v", gotHealthy)
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
