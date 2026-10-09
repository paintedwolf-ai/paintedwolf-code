package editordoc

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestAgentReadBasisIsChatScopedAndNeverRefreshesItself(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "first\nsecond\n"})
	read, _, err := f.service.OpenText(t.Context(), f.project.ID, f.project.SourceBranch, f.rootID, "a.txt")
	testutil.FailErr(t, "read initial document", err)
	f.service.RememberAgentRead(f.project.ID, "chat-a", read.ID, read.Revision)
	_, err = f.service.ReplaceSnapshot(t.Context(), read.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: read.Revision},
		Content:         "first\nsecond\nhuman typing\n", EOL: "lf",
	})
	testutil.FailErr(t, "type after read", err)
	_, _, err = f.service.OpenText(t.Context(), f.project.ID, f.project.SourceBranch, f.rootID, "a.txt")
	testutil.FailErr(t, "internal current read", err)
	basis, err := f.service.AgentReadBase(t.Context(), f.project.ID, "chat-a", read.ID, nil)
	testutil.FailErr(t, "load original agent basis", err)
	if basis.Revision != read.Revision || basis.Draft != read.Draft {
		t.Fatalf("adopted unseen text: %+v", basis)
	}
	if _, err := f.service.AgentReadBase(t.Context(), f.project.ID, "chat-b", read.ID, nil); !errors.Is(err, ErrAgentReadRequired) {
		t.Fatalf("another chat borrowed the read: %v", err)
	}
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(basis, "FIRST\nsecond\n"))
	testutil.FailErr(t, "rebase whole-file proposal", err)
	if result.Document.Draft != "FIRST\nsecond\nhuman typing\n" || f.disk(t, "a.txt") != result.Document.Draft {
		t.Fatalf("lost typing: %+v", result)
	}
}

func TestAgentReadBasisRequiresRereadAfterPinExpiryOrRestart(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "fooBar\n"})
	read, _, err := f.service.OpenText(t.Context(), f.project.ID, f.project.SourceBranch, f.rootID, "a.txt")
	testutil.FailErr(t, "read initial document", err)
	f.service.RememberAgentRead(f.project.ID, "chat-a", read.ID, read.Revision)
	_, err = f.service.ReplaceSnapshot(t.Context(), read.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: read.Revision}, Content: "fooBaz\n", EOL: "lf",
	})
	testutil.FailErr(t, "type on agent line", err)
	basis, err := f.service.AgentReadBase(t.Context(), f.project.ID, "chat-a", read.ID, nil)
	testutil.FailErr(t, "load agent read", err)
	if _, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(basis, "fetchBar\n")); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("merged unseen identifier: %v", err)
	}
	_, err = f.store.db.ExecContext(t.Context(), `DELETE FROM editor_document_snapshots WHERE document_id=? AND revision=?`, read.ID, read.Revision)
	testutil.FailErr(t, "expire read pin", err)
	if _, err := f.service.AgentReadBase(t.Context(), f.project.ID, "chat-a", read.ID, nil); !errors.Is(err, ErrAgentReadRequired) {
		t.Fatalf("expired pin adopted current: %v", err)
	}
	restarted := New(f.store, f.recorder, f.recorder.History, fixedRoots{p: f.project})
	closeServiceAtCleanup(t, restarted)
	if _, err := restarted.AgentReadBase(t.Context(), f.project.ID, "chat-a", read.ID, nil); !errors.Is(err, ErrAgentReadRequired) {
		t.Fatalf("restart adopted current: %v", err)
	}
}

func TestToolBatchCannotAdoptItsOwnRead(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "first\nsecond\n"})
	read, _, err := f.service.OpenText(t.Context(), f.project.ID, f.project.SourceBranch, f.rootID, "a.txt")
	testutil.FailErr(t, "open initial document", err)
	empty := frozenBases(f.service.FreezeAgentReads(f.project.ID, "chat-a"))
	f.service.RememberAgentRead(f.project.ID, "chat-a", read.ID, read.Revision)
	if _, err := f.service.AgentReadBase(t.Context(), f.project.ID, "chat-a", read.ID, empty); !errors.Is(err, ErrAgentReadRequired) {
		t.Fatalf("same batch adopted its own first read: %v", err)
	}
	frozen := frozenBases(f.service.FreezeAgentReads(f.project.ID, "chat-a"))
	changed, err := f.service.ReplaceSnapshot(t.Context(), read.ID, f.project.ID, SnapshotReplacement{
		DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: read.Revision},
		Content:         "first\nsecond\nhuman\n", EOL: "lf",
	})
	testutil.FailErr(t, "type between model requests", err)
	f.service.RememberAgentRead(f.project.ID, "chat-a", read.ID, changed.Revision)
	basis, err := f.service.AgentReadBase(t.Context(), f.project.ID, "chat-a", read.ID, frozen)
	testutil.FailErr(t, "load response basis", err)
	if basis.Revision != read.Revision {
		t.Fatalf("batch adopted unseen read: %+v", basis)
	}
	next := frozenBases(f.service.FreezeAgentReads(f.project.ID, "chat-a"))
	basis, err = f.service.AgentReadBase(t.Context(), f.project.ID, "chat-a", read.ID, next)
	testutil.FailErr(t, "load next response basis", err)
	if basis.Revision != changed.Revision {
		t.Fatalf("next batch lost completed read: %+v", basis)
	}
}

// frozenBases answers a response's frozen read set the way the tool layer does.
func frozenBases(revisions map[string]int64) ReadBasisLookup {
	return func(documentID string) (int64, bool) {
		revision, ok := revisions[documentID]
		return revision, ok
	}
}
