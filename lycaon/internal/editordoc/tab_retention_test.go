package editordoc

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSuspendedTabProtectsRootWithoutPresence(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	testutil.FailErr(t, "retain tab", f.service.ReplaceRetention(t.Context(), f.project.ID, "window:main", []string{d.ID}, nil, nil))
	f.service.ForgetRemoved([]string{d.ID})
	dependents, err := f.service.LifecycleDependents(t.Context(), f.project.ID, f.rootID)
	testutil.FailErr(t, "read root dependents", err)
	if len(dependents) != 1 || dependents[0].ID != d.ID || dependents[0].Draft != "" {
		t.Fatalf("suspended dependents = %+v", dependents)
	}
	testutil.FailErr(t, "close tab", f.service.ReplaceRetention(t.Context(), f.project.ID, "window:main", nil, nil, nil))
	dependents, err = f.service.LifecycleDependents(t.Context(), f.project.ID, f.rootID)
	testutil.FailErr(t, "read closed dependents", err)
	if len(dependents) != 0 {
		t.Fatalf("closed dependents = %+v", dependents)
	}
}

func TestRetentionReconcilesOrphanWindowsAndPreservesOtherPeople(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	testutil.FailErr(t, "retain lost window", f.service.ReplaceRetention(t.Context(), f.project.ID, "window:lost", []string{d.ID}, nil, nil))
	testutil.FailErr(t, "retain restored window", f.service.ReplaceRetention(t.Context(), f.project.ID, "window:restored", []string{d.ID}, nil, nil))
	testutil.FailErr(t, "reconcile windows", f.service.ReplaceRetention(t.Context(), f.project.ID, "window:main", nil, []string{"window:main", "window:restored"}, nil))
	var count int
	testutil.FailErr(t, "count retained windows", f.store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM editor_document_retention WHERE document_id=?`, d.ID).Scan(&count))
	if count != 1 {
		t.Fatalf("retained windows = %d", count)
	}
	stranger := people.WithCaller(t.Context(), people.Person{ID: uuid.NewString(), Role: api.PersonRoleOwner})
	if err := f.service.ReplaceRetention(stranger, f.project.ID, "window:restored", nil, nil, nil); !errors.Is(err, ErrReplicaIdentity) {
		t.Fatalf("foreign release = %v", err)
	}
}

func TestStatusesDoNotAdmitDocumentsOrReconcileDisk(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	f.service.replicas.mu.Lock()
	f.service.replicas.evict(t.Context(), d.ID)
	f.service.replicas.mu.Unlock()
	f.write(t, "a.txt", "external")
	missing := uuid.NewString()
	result, err := f.service.Statuses(t.Context(), f.project.ID, []string{d.ID, missing})
	testutil.FailErr(t, "read metadata", err)
	if len(result.Documents) != 1 || result.Documents[0].Revision != d.Revision || len(result.Missing) != 1 || result.Missing[0] != missing {
		t.Fatalf("metadata = %+v", result)
	}
	f.service.replicas.mu.Lock()
	defer f.service.replicas.mu.Unlock()
	if len(f.service.replicas.entries) != 0 {
		t.Fatal("metadata read admitted a replica")
	}
}

func TestStaleInventoryKeepsMarksOfClientsWithLiveStreams(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	testutil.FailErr(t, "retain peer tab", f.service.ReplaceRetention(t.Context(), f.project.ID, "window:peer", []string{d.ID}, nil, nil))
	testutil.FailErr(t, "retain crashed tab", f.service.ReplaceRetention(t.Context(), f.project.ID, "window:crashed", []string{d.ID}, nil, nil))
	live := func(clientID string) bool { return clientID == "window:peer" }
	testutil.FailErr(t, "publish stale inventory", f.service.ReplaceRetention(t.Context(), f.project.ID, "window:main", nil, []string{"window:main"}, live))
	rows, err := f.store.db.QueryContext(t.Context(), `SELECT client_id FROM editor_document_retention WHERE document_id=? ORDER BY client_id`, d.ID)
	testutil.FailErr(t, "list retained clients", err)
	defer func() { _ = rows.Close() }()
	var clients []string
	for rows.Next() {
		var client string
		testutil.FailErr(t, "scan retained client", rows.Scan(&client))
		clients = append(clients, client)
	}
	testutil.FailErr(t, "iterate retained clients", rows.Err())
	if len(clients) != 1 || clients[0] != "window:peer" {
		t.Fatalf("retained clients after stale inventory = %v", clients)
	}
}

func TestRetentionInventoryCannotEraseOtherRuntimeOrItsOwnLiveReference(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	for _, client := range []string{"window:main", "browser:closed", "browser:live"} {
		testutil.FailErr(t, "retain runtime tab", f.service.ReplaceRetention(t.Context(), f.project.ID, client, []string{d.ID}, nil, nil))
	}
	if err := f.service.ReplaceRetention(t.Context(), f.project.ID, "window:main", nil, []string{}, nil); !errors.Is(err, ErrReplicaIdentity) {
		t.Fatalf("empty inventory accepted: %v", err)
	}
	testutil.FailErr(t, "sweep browser orphan", f.service.ReplaceRetention(t.Context(), f.project.ID, "browser:live", []string{d.ID}, []string{"browser:live"}, nil))
	var count int
	testutil.FailErr(t, "count retained runtimes", f.store.db.QueryRowContext(t.Context(), `SELECT count(*) FROM editor_document_retention WHERE document_id=?`, d.ID).Scan(&count))
	if count != 2 {
		t.Fatalf("cross-runtime reference count = %d", count)
	}
}
