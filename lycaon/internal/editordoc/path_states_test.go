package editordoc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnsavedPathMetadataIncludesDeletedDraftWithoutOpeningText(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base", "clean.txt": "clean"})
	d := f.open(t, "a.txt")
	_ = f.open(t, "clean.txt")
	typed, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "human", EOL: "lf"})
	testutil.FailErr(t, "human draft", err)
	testutil.FailErr(t, "remove saved file", os.Remove(filepath.Join(f.root, "a.txt")))
	testutil.FailErr(t, "observe deletion", f.service.ObserveExternal(t.Context(), f.project, []PathRef{{RootID: f.rootID, Path: "a.txt"}}))
	stored, err := f.store.Get(t.Context(), typed.ID)
	testutil.FailErr(t, "accepted absent identity", err)
	f.service.replicas.mu.Lock()
	for id := range f.service.replicas.entries {
		f.service.replicas.evict(t.Context(), id)
	}
	f.service.replicas.mu.Unlock()
	var states []DocumentPathState
	testutil.FailErr(t, "unsaved metadata", f.service.VisitUnsavedDocuments(t.Context(), f.project.ID, f.project.SourceBranch, f.rootID, "", true, func(state DocumentPathState) error {
		states = append(states, state)
		return nil
	}))
	if len(states) != 1 || states[0].ID != typed.ID || !states[0].Absent || states[0].Revision != stored.Revision {
		t.Fatalf("unsaved metadata=%+v", states)
	}
	f.service.replicas.mu.Lock()
	defer f.service.replicas.mu.Unlock()
	if len(f.service.replicas.entries) != 0 {
		t.Fatal("metadata lookup opened a replica")
	}
}
