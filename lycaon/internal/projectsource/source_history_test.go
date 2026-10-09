package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceHistoryRenameUndoRedo(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed source", os.MkdirAll(filepath.Join(root, "src"), 0o750))
	testutil.FailErr(t, "write source", os.WriteFile(filepath.Join(root, "src", "note.txt"), []byte("kept"), 0o640))
	entryID := uuid.NewString()
	_, err := service.Rename(t.Context(), entryID, p, SourceRenameRequest{
		RootID: p.Roots[0].ID, From: "src/note.txt", To: "archive/note.txt",
	})
	testutil.FailErr(t, "move source", err)

	state, err := service.History.State(t.Context(), p.ID)
	testutil.FailErr(t, "read history", err)
	if state.Undo == nil || state.Undo.ID != entryID || state.Undo.Kind != "move" {
		t.Fatalf("undo = %+v", state.Undo)
	}
	if state.Redo != nil {
		t.Fatalf("redo = %+v, want nil", state.Redo)
	}

	result, err := service.Undo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: entryID})
	testutil.FailErr(t, "undo move", err)
	if result.Op != "rename" || result.FromPath != "archive/note.txt" || result.Path != "src/note.txt" {
		t.Fatalf("undo result = %+v", result)
	}
	assertSourceHistoryFile(t, root, "src/note.txt", "kept")
	state, err = service.History.State(t.Context(), p.ID)
	testutil.FailErr(t, "read undone history", err)
	if state.Undo != nil || state.Redo == nil || state.Redo.ID != entryID {
		t.Fatalf("undone state = %+v", state)
	}

	_, err = service.Redo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: entryID})
	testutil.FailErr(t, "redo move", err)
	assertSourceHistoryFile(t, root, "archive/note.txt", "kept")
}

func TestSourceHistoryLifecycleOperationsRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	service, p, root, _ := sourceMutationFixture(t)
	rootID := p.Roots[0].ID

	createID := uuid.NewString()
	_, err := service.Create(t.Context(), createID, p, SourceEntryCreateRequest{
		RootID: rootID, Path: "empty", Kind: SourceEntryFolder,
	})
	testutil.FailErr(t, "create folder", err)
	undoHistoryHead(t, service, p, createID)
	if _, err := os.Stat(filepath.Join(root, "empty")); !os.IsNotExist(err) {
		t.Fatalf("created folder remained after undo: %v", err)
	}
	redoHistoryHead(t, service, p, createID)
	info, err := os.Stat(filepath.Join(root, "empty"))
	testutil.FailErr(t, "stat redone folder", err)
	if !info.IsDir() {
		t.Fatal("redone create is not a folder")
	}

	testutil.FailErr(t, "write copy source", os.WriteFile(filepath.Join(root, "source.txt"), []byte("copy"), 0o640))
	copyID := uuid.NewString()
	_, err = service.Copy(t.Context(), copyID, p, SourceCopyRequest{
		RootID: rootID, From: "source.txt", To: "copy.txt",
	})
	testutil.FailErr(t, "copy source", err)
	undoHistoryHead(t, service, p, copyID)
	if _, err := os.Stat(filepath.Join(root, "copy.txt")); !os.IsNotExist(err) {
		t.Fatalf("copy remained after undo: %v", err)
	}
	redoHistoryHead(t, service, p, copyID)
	assertSourceHistoryFile(t, root, "copy.txt", "copy")

	deleteID := uuid.NewString()
	err = service.Delete(t.Context(), deleteID, p, SourceDeleteRequest{
		RootID: rootID, Path: "copy.txt",
	})
	testutil.FailErr(t, "move copy to trash", err)
	undoHistoryHead(t, service, p, deleteID)
	assertSourceHistoryFile(t, root, "copy.txt", "copy")
	redoHistoryHead(t, service, p, deleteID)
	if _, err := os.Stat(filepath.Join(root, "copy.txt")); !os.IsNotExist(err) {
		t.Fatalf("file remained after redone trash: %v", err)
	}
}

func TestSourceHistoryRefusesDivergedUndoAndStaleHead(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "note.txt"), []byte("before"), 0o640))
	entryID := uuid.NewString()
	_, err := service.Rename(t.Context(), entryID, p, SourceRenameRequest{
		RootID: p.Roots[0].ID, From: "note.txt", To: "moved.txt",
	})
	testutil.FailErr(t, "rename source", err)
	if _, err := service.Undo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: uuid.NewString()}); !errors.Is(err, ErrSourceHistoryChanged) {
		t.Fatalf("stale head error = %v", err)
	}
	testutil.FailErr(t, "replace moved source", os.Rename(filepath.Join(root, "moved.txt"), filepath.Join(root, "original.txt")))
	testutil.FailErr(t, "create replacement", os.WriteFile(filepath.Join(root, "moved.txt"), []byte("changed"), 0o640))
	if _, err := service.Undo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: entryID}); !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("diverged undo error = %v", err)
	}
	state, err := service.History.State(t.Context(), p.ID)
	testutil.FailErr(t, "read retained history", err)
	if state.Undo == nil || state.Undo.ID != entryID || state.Redo != nil {
		t.Fatalf("history changed after refused undo: %+v", state)
	}
}

func TestSourceHistoryReplayAndNewBranch(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed first source", os.WriteFile(filepath.Join(root, "one.txt"), []byte("one"), 0o640))
	firstID := uuid.NewString()
	_, err := service.Rename(t.Context(), firstID, p, SourceRenameRequest{
		RootID: p.Roots[0].ID, From: "one.txt", To: "moved.txt",
	})
	testutil.FailErr(t, "rename first source", err)
	undoID := uuid.NewString()
	request := SourceHistoryMutationRequest{ExpectedEntryID: firstID}
	first, err := service.Undo(t.Context(), undoID, p, request)
	testutil.FailErr(t, "undo first source", err)
	replayed, err := service.Undo(t.Context(), undoID, p, request)
	testutil.FailErr(t, "replay undo", err)
	if *replayed != *first {
		t.Fatalf("replayed undo = %+v want %+v", replayed, first)
	}

	secondID := uuid.NewString()
	_, err = service.Create(t.Context(), secondID, p, SourceEntryCreateRequest{
		RootID: p.Roots[0].ID, Path: "new.txt", Kind: SourceEntryFile,
	})
	testutil.FailErr(t, "create new history branch", err)
	state, err := service.History.State(t.Context(), p.ID)
	testutil.FailErr(t, "read new history branch", err)
	if state.Undo == nil || state.Undo.ID != secondID || state.Redo != nil {
		t.Fatalf("new history branch = %+v", state)
	}
	if _, err := service.Redo(t.Context(), uuid.NewString(), p, request); !errors.Is(err, ErrSourceHistoryChanged) {
		t.Fatalf("discarded redo error = %v", err)
	}
}

func undoHistoryHead(t *testing.T, service *SourceMutationService, p *Project, entryID string) {
	t.Helper()
	_, err := service.Undo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: entryID})
	testutil.FailErr(t, "undo history head", err)
}

func redoHistoryHead(t *testing.T, service *SourceMutationService, p *Project, entryID string) {
	t.Helper()
	_, err := service.Redo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: entryID})
	testutil.FailErr(t, "redo history head", err)
}

func assertSourceHistoryFile(t *testing.T, root, rel, want string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	testutil.FailErr(t, "read history file", err)
	if string(body) != want {
		t.Fatalf("%s = %q want %q", rel, body, want)
	}
}
