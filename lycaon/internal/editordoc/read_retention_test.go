//go:build integration

package editordoc

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReadOnlyDocumentsAreBoundedWithoutDeletingJoinedIdentities(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < readDocumentCount+5; i++ {
		files[fmt.Sprintf("%d.txt", i)] = "base\n"
	}
	f := newExternalFixture(t, files)
	joined, err := f.service.Open(t.Context(), f.project, "0.txt", f.rootID, "", "window", nil)
	testutil.FailErr(t, "open window", err)
	_, err = f.service.Join(t.Context(), joined.ID, f.project.ID, ReplicaJoin{ClientID: "window", Incarnation: "first", Epoch: joined.Epoch})
	testutil.FailErr(t, "join window", err)
	testutil.FailErr(t, "disconnect window", f.service.Leave(t.Context(), joined.ID, f.project.ID, "window", "first"))
	var oldest *Document
	for i := 1; i < readDocumentCount+5; i++ {
		d, ok, err := f.service.OpenText(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, fmt.Sprintf("%d.txt", i))
		testutil.FailErr(t, "read file", err)
		if !ok {
			t.Fatal("read did not return a document")
		}
		if i == 1 {
			oldest = d
			f.write(t, "1.txt", "outside update\n")
			_, err = f.service.ObserveDisk(t.Context(), f.project, d.ID, "")
			testutil.FailErr(t, "observe read-only file", err)
		}
	}
	_, err = f.store.Get(t.Context(), joined.ID)
	testutil.FailErr(t, "retain disconnected identity", err)
	if _, err := f.store.Get(t.Context(), oldest.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old read cache still retained: %v", err)
	}
	var count int
	testutil.FailErr(t, "count documents", f.store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM editor_documents`).Scan(&count))
	if count > readDocumentCount+1 {
		t.Fatalf("read cache retained %d documents", count)
	}
}

func TestReadOnlyDocumentsRespectRetainedByteBudget(t *testing.T) {
	const filesCount = 16
	files := make(map[string]string, filesCount)
	for i := 0; i < filesCount; i++ {
		files[fmt.Sprintf("%d.txt", i)] = strings.Repeat("a", 1<<20)
	}
	f := newExternalFixture(t, files)
	var oldest, latest *Document
	for i := 0; i < filesCount; i++ {
		d, ok, err := f.service.OpenText(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, fmt.Sprintf("%d.txt", i))
		testutil.FailErr(t, "read large file", err)
		if !ok {
			t.Fatal("large text file did not open")
		}
		if oldest == nil {
			oldest = d
		}
		latest = d
	}
	if _, err := f.store.Get(t.Context(), oldest.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("byte budget retained the oldest large document: %v", err)
	}
	_, err := f.store.Get(t.Context(), latest.ID)
	testutil.FailErr(t, "retain current large read", err)
}

func TestColdCleanDocumentReconcilesWhenReadAgain(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base\n"})
	d := f.open(t, "a.txt")
	f.service.replicas.mu.Lock()
	f.service.replicas.evict(t.Context(), d.ID)
	f.service.replicas.mu.Unlock()
	f.write(t, "a.txt", "outside\n")
	testutil.FailErr(t, "resync", f.service.ObserveExternal(t.Context(), f.project, nil))
	stored, err := f.store.Get(t.Context(), d.ID)
	testutil.FailErr(t, "read cold state", err)
	if stored.Revision != d.Revision {
		t.Fatal("resync imported cold history")
	}
	reopened, ok, err := f.service.OpenText(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, "a.txt")
	testutil.FailErr(t, "reopen cold document", err)
	if !ok || reopened.Draft != "outside\n" || reopened.ID != d.ID {
		t.Fatalf("cold document did not reconcile: %+v", reopened)
	}
}

// Retention follows opens, not writes: a disk change landing in a document
// nobody has opened lately does not buy it a place ahead of one just reopened.
func TestReadOnlyDocumentsEvictLeastRecentlyOpened(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < readDocumentCount+2; i++ {
		files[fmt.Sprintf("%d.txt", i)] = "base\n"
	}
	f := newExternalFixture(t, files)
	opened := map[int]*Document{}
	open := func(i int) {
		t.Helper()
		d, ok, err := f.service.OpenText(t.Context(), f.project.ID, sourcebranch.Trunk, f.rootID, fmt.Sprintf("%d.txt", i))
		testutil.FailErr(t, "read file", err)
		if !ok {
			t.Fatal("read did not return a document")
		}
		opened[i] = d
	}
	retained := func(i int) bool {
		t.Helper()
		_, err := f.store.Get(t.Context(), opened[i].ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatalf("get %d.txt: %v", i, err)
		}
		return err == nil
	}
	for i := 0; i < readDocumentCount; i++ {
		open(i)
	}
	f.write(t, "0.txt", "outside update\n")
	_, err := f.service.ObserveDisk(t.Context(), f.project, opened[0].ID, "")
	testutil.FailErr(t, "observe disk change", err)
	open(1)

	open(readDocumentCount)
	if retained(0) {
		t.Fatal("a disk write kept the least recently opened document")
	}
	if !retained(1) || !retained(2) {
		t.Fatal("evicted a more recently opened document")
	}
	open(readDocumentCount + 1)
	if retained(2) || !retained(1) {
		t.Fatal("want the next least recently opened document evicted and the reopened one kept")
	}
}
