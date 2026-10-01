package editordoc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestObservationPagesSelectedColdDocumentsWithoutAdmittingUnrelatedBodies(t *testing.T) {
	f := newExternalFixture(t, nil)
	testutil.FailErr(t, "create selected subtree", os.Mkdir(filepath.Join(f.root, "selected"), 0o700))
	documents := make([]*Document, 0, observationPageSize+6)
	for i := 0; i < observationPageSize+6; i++ {
		path := fmt.Sprintf("selected/%03d.txt", i)
		f.write(t, path, "base\n")
		d, err := f.service.Open(t.Context(), f.project, path, f.rootID, "", "window:main", nil)
		testutil.FailErr(t, "open retained tab", err)
		documents = append(documents, d)
	}
	f.write(t, "selected/closed.txt", "closed\n")
	closed := f.open(t, "selected/closed.txt")
	f.write(t, "selected/closed.txt", "closed changed\n")
	f.write(t, "unrelated.txt", "unrelated\n")
	unrelated := f.open(t, "unrelated.txt")
	ids := []string{unrelated.ID, closed.ID}
	for _, d := range documents {
		ids = append(ids, d.ID)
		f.write(t, d.Path, "outside\n")
	}
	f.service.ForgetRemoved(ids)
	f.service.replicas.mu.Lock()
	for _, id := range ids {
		f.service.replicas.evict(t.Context(), id)
	}
	f.service.replicas.mu.Unlock()
	empty, err := f.service.observationQuery(f.project, nil)
	testutil.FailErr(t, "select idle observation scope", err)
	page, err := f.store.observedDocumentPage(t.Context(), empty, "")
	testutil.FailErr(t, "read idle observation identities", err)
	if len(page) != observationPageSize {
		t.Fatalf("retained cold tabs missing from reconciliation: %d", len(page))
	}
	testutil.FailErr(t, "observe selected cold subtree", f.service.ObserveExternal(t.Context(), f.project, []PathRef{{RootID: f.rootID, Path: "selected"}}))
	for _, d := range documents {
		got, err := f.store.Get(t.Context(), d.ID)
		testutil.FailErr(t, "read reconciled identity", err)
		if got.Draft != "outside\n" || got.Revision != d.Revision+1 {
			t.Fatalf("identity page skipped %s", d.Path)
		}
	}
	f.service.replicas.mu.Lock()
	_, admitted := f.service.replicas.entries[unrelated.ID]
	f.service.replicas.mu.Unlock()
	closedAfter, err := f.store.Get(t.Context(), closed.ID)
	testutil.FailErr(t, "read unreferenced closed document", err)
	if closedAfter.Revision != closed.Revision {
		t.Fatal("observation reloaded an unreferenced clean document")
	}
	if admitted {
		t.Fatal("subtree observation admitted unrelated text")
	}
}
