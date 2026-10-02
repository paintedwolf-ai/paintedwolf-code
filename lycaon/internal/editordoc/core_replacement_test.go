package editordoc

import (
	"testing"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/testutil"
)

// A core that ends — a crash, a timeout, an exhausted memory ceiling — takes
// its resident documents with it; the next edit starts a fresh core and
// reloads the document from its accepted state.
func TestEditingContinuesAfterTheDocumentCoreEnds(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "base"})
	d := f.open(t, "a.txt")
	first, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision},
		Content:         "base one", EOL: "lf",
	})
	testutil.FailErr(t, "accept first edit", err)

	f.service.replicas.mu.Lock()
	ended := f.service.replicas.engine
	f.service.replicas.mu.Unlock()
	testutil.FailErr(t, "end the document core", ended.Close(t.Context()))

	second, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: first.Revision},
		Content:         "base one two", EOL: "lf",
	})
	testutil.FailErr(t, "accept an edit after the core ended", err)
	if second.Draft != "base one two" || second.Revision <= first.Revision {
		t.Fatalf("edit after the core ended = %q at revision %d, want the new draft past %d", second.Draft, second.Revision, first.Revision)
	}
	f.service.replicas.mu.Lock()
	replaced := f.service.replicas.engine
	f.service.replicas.mu.Unlock()
	if replaced == ended || replaced.Closed() {
		t.Fatal("the editor kept the ended core instead of starting a fresh one")
	}
}
