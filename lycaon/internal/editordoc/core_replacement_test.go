package editordoc

import (
	"errors"
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

func TestEditorDocSystemicInvariants(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "initial\n"})
	d := f.open(t, "a.txt")

	// Invariant 1: Save replay with identical parameters returns the committed
	// document without advancing revision, while altered parameters conflict.
	edited, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window:1", OperationID: uuid.NewString(), ExpectedRevision: d.Revision},
		Content:         "published\n", EOL: "lf",
	})
	testutil.FailErr(t, "stage edit for save invariant", err)
	saveOp := uuid.NewString()
	saved, err := f.service.Save(t.Context(), f.project, d.ID, "window:1", saveOp, "sess-1", 1, edited.Revision)
	testutil.FailErr(t, "initial save", err)
	replayed, err := f.service.Save(t.Context(), f.project, d.ID, "window:1", saveOp, "sess-1", 1, edited.Revision)
	testutil.FailErr(t, "idempotent save replay", err)
	if replayed.Revision != saved.Revision || replayed.BaseSHA256 != saved.BaseSHA256 || replayed.Dirty {
		t.Fatalf("save replay altered state: saved=%+v replayed=%+v", saved, replayed)
	}
	if _, err := f.service.Save(t.Context(), f.project, d.ID, "window:1", saveOp, "sess-1", 1, saved.Revision); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("mismatched save replay revision error = %v, want ErrOperationConflict", err)
	}
	if _, err := f.service.Save(t.Context(), f.project, d.ID, "window:2", saveOp, "sess-1", 1, edited.Revision); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("mismatched save replay client error = %v, want ErrOperationConflict", err)
	}

	// Invariant 2: DisconnectClient drops live presence and client-person binding
	// without discarding durable tab retention or unsaved drafts.
	_, err = f.service.Join(t.Context(), d.ID, f.project.ID, ReplicaJoin{ClientID: "window:1", Incarnation: "inc-1", Epoch: saved.Epoch})
	testutil.FailErr(t, "join presence", err)
	testutil.FailErr(t, "replace retention", f.service.ReplaceRetention(t.Context(), f.project.ID, "window:1", []string{d.ID}, []string{"window:1"}, nil))
	dirty, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window:1", OperationID: uuid.NewString(), ExpectedRevision: saved.Revision},
		Content:         "unsaved draft\n", EOL: "lf",
	})
	testutil.FailErr(t, "create unsaved draft", err)
	f.service.DisconnectClient(t.Context(), "window:1")
	snap, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, d.ID)
	testutil.FailErr(t, "snapshot after disconnect", err)
	if len(snap.Participants) != 0 || snap.Draft != "unsaved draft\n" || !snap.Dirty || snap.Revision != dirty.Revision {
		t.Fatalf("disconnect altered durable draft or kept presence: %+v", snap)
	}
	dependents, err := f.service.LifecycleDependents(t.Context(), f.project.ID, d.RootID)
	testutil.FailErr(t, "lifecycle dependents after disconnect", err)
	if len(dependents) != 1 || dependents[0].ID != d.ID || !dependents[0].Dirty {
		t.Fatalf("lifecycle dependents = %+v, want retained dirty document %s", dependents, d.ID)
	}

	// Invariant 3: Nil-receiver entrypoints never panic and return empty/not-found.
	var nilService *Service
	if rev, ok, err := nilService.CurrentRevision(t.Context(), f.project, d.RootID, d.Path); err != nil || ok || rev != 0 {
		t.Fatalf("nil CurrentRevision = (%d, %v, %v)", rev, ok, err)
	}
	if deps, err := nilService.LifecycleDependents(t.Context(), f.project.ID, d.RootID); err != nil || len(deps) != 0 {
		t.Fatalf("nil LifecycleDependents = (%v, %v)", deps, err)
	}
}
