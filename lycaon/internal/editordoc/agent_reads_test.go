package editordoc

import (
	"errors"
	"github.com/lycaon/lycaon/internal/documentcore"
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
	result, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(basis.Document, "FIRST\nsecond\n"))
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
	if _, err := f.service.ApplyAgentEdit(t.Context(), agentEdit(basis.Document, "fetchBar\n")); !errors.Is(err, ErrRevisionConflict) {
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
func frozenBases(revisions map[string]documentcore.ReadBasis) ReadBasisLookup {
	return func(documentID string) (documentcore.ReadBasis, bool) {
		revision, ok := revisions[documentID]
		return revision, ok
	}
}

func TestFrozenPartialReadNeverWidensToLaterCoverage(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "one\ntwo\nthree\n"})
	doc := f.open(t, "a.txt")
	f.service.RememberAgentReadSpans(f.project.ID, "chat-a", doc.ID, doc.Revision, []documentcore.LineSpan{{StartLine: 2, EndLine: 2}})
	frozen := frozenBases(f.service.FreezeAgentReads(f.project.ID, "chat-a"))
	f.service.RememberAgentRead(f.project.ID, "chat-a", doc.ID, doc.Revision)
	basis, err := f.service.AgentReadBase(t.Context(), f.project.ID, "chat-a", doc.ID, frozen)
	testutil.FailErr(t, "frozen partial read", err)
	if basis.ReadWhole || len(basis.ReadSpans) != 1 || basis.ReadSpans[0].StartLine != 2 || basis.ReadSpans[0].EndLine != 2 {
		t.Fatalf("frozen coverage widened=%+v", basis)
	}
	current, err := f.service.AgentReadBase(t.Context(), f.project.ID, "chat-a", doc.ID, nil)
	testutil.FailErr(t, "current read", err)
	if !current.ReadWhole {
		t.Fatal("completed whole read was lost")
	}
}
