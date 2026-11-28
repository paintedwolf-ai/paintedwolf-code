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

func TestAgentReadByteCoverageRequiresOneCompleteSource(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "0123456789\n"})
	doc := f.open(t, "a.txt")
	const chat = "chat-a"
	source := documentcore.ReadSource{ID: "a.txt", SHA256: "original", Representation: "utf8-no-bom", Bytes: 11}
	remember := func(s documentcore.ReadSource, start, end int64) {
		f.service.RememberAgentReadBytes(f.project.ID, chat, doc.ID, doc.Revision, s, start, end)
	}
	wantUnread := func(lookup ReadBasisLookup) {
		t.Helper()
		if _, err := f.service.AgentReadBase(t.Context(), f.project.ID, chat, doc.ID, lookup); !errors.Is(err, ErrAgentReadRequired) {
			t.Fatalf("incomplete delivery granted a basis: %v", err)
		}
	}

	remember(source, 0, 5)
	other := source
	other.Representation = "logical-utf8-lf"
	remember(other, 5, 11)
	wantUnread(nil)

	remember(source, 8, 11)
	remember(source, 0, 4)
	wantUnread(nil)
	frozen := frozenBases(f.service.FreezeAgentReads(f.project.ID, chat))
	remember(source, 3, 9)
	wantUnread(frozen)

	basis, err := f.service.AgentReadBase(t.Context(), f.project.ID, chat, doc.ID, nil)
	testutil.FailErr(t, "load fully delivered read", err)
	if !basis.ReadWhole || basis.Revision != doc.Revision || basis.Draft != doc.Draft {
		t.Fatalf("whole basis does not match the delivered document: %+v", basis)
	}
	if _, err := f.service.AgentReadBase(t.Context(), f.project.ID, "chat-b", doc.ID, nil); !errors.Is(err, ErrAgentReadRequired) {
		t.Fatalf("another chat borrowed the delivered range: %v", err)
	}
}

func TestAgentReadByteCoverageDoesNotCarryAcrossRevisions(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "0123456789\n"})
	doc := f.open(t, "a.txt")
	const chat = "chat-a"
	source := documentcore.ReadSource{ID: "a.txt", SHA256: "same-bytes", Representation: "utf8-no-bom", Bytes: 11}
	f.service.RememberAgentReadBytes(f.project.ID, chat, doc.ID, doc.Revision, source, 0, 5)
	f.service.RememberAgentReadBytes(f.project.ID, chat, doc.ID, doc.Revision+1, source, 5, 11)
	f.service.RememberAgentReadBytes(f.project.ID, chat, doc.ID, doc.Revision, source, 5, 11)
	if _, ok := f.service.FreezeAgentReads(f.project.ID, chat)[doc.ID]; ok {
		t.Fatal("ranges from different revisions granted a basis")
	}
}

func TestAgentReadByteCoverageDropsExcessDisjointRanges(t *testing.T) {
	f := newAgentFixture(t, map[string]string{"a.txt": "0123456789\n"})
	doc := f.open(t, "a.txt")
	const chat = "chat-a"
	limit := int64(maxAgentReadByteRanges)
	source := documentcore.ReadSource{ID: "a.txt", SHA256: "source", Representation: "utf8-no-bom", Bytes: 2*limit + 1}
	for i := int64(0); i <= limit; i++ {
		f.service.RememberAgentReadBytes(f.project.ID, chat, doc.ID, doc.Revision, source, 2*i, 2*i+1)
	}
	key := agentReadKey{project: f.project.ID, session: chat, document: doc.ID}
	if ranges := f.service.agentReads.bases[key].pending.ranges; len(ranges) != 0 {
		t.Fatalf("too many disjoint ranges were retained: %d", len(ranges))
	}
}
