package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type adapterRoots struct{ p *project.Project }

func (r adapterRoots) Get(context.Context, string) (*project.Project, error) { return r.p, nil }

func newAdapterFixture(t *testing.T) (editorDocumentsAdapter, *editordoc.Service, *project.Project, string) {
	t.Helper()
	sqlDB := testdbfixture.Open(t, "store.db")
	root := t.TempDir()
	projectID, rootID := testdbseed.DefaultProjectID, uuid.NewString()
	testdbseed.InsertProjectRootWithID(t, sqlDB, projectID, rootID, root)
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\n"), 0o644))
	p := &project.Project{ID: projectID, Roots: []project.Root{{ID: rootID, ProjectID: projectID, Path: root, IsPrimary: true}}}
	service := editordoc.New(editordoc.NewStore(sqlDB), sourceledger.New(sqlDB, ""), adapterRoots{p: p})
	return editorDocumentsAdapter{service: service}, service, p, rootID
}

// The adapter presents an open document's text and identity, and a stale
// apply as the tools' own moved sentinel.
func TestEditorDocumentsAdapterServesAndLandsDocuments(t *testing.T) {
	adapter, service, p, rootID := newAdapterFixture(t)
	ctx := t.Context()
	if _, ok, err := adapter.OpenDocument(ctx, p.ID, p.SourceBranch, rootID, "a.txt"); err != nil || !ok {
		t.Fatalf("closed path did not start a document: ok=%v err=%v", ok, err)
	}
	opened, err := service.Open(ctx, p, "a.txt", rootID, "", "window", nil)
	testutil.FailErr(t, "open", err)
	typed, err := service.ReplaceSnapshot(ctx, opened.ID, p.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: opened.Revision}, Content: "typed\n", EOL: "lf", MixedEOL: false})
	testutil.FailErr(t, "type", err)

	doc, ok, err := adapter.OpenDocument(ctx, p.ID, p.SourceBranch, rootID, "a.txt")
	testutil.FailErr(t, "open document", err)
	if !ok || doc.Text != "typed\n" || !doc.Dirty || doc.Revision != typed.Revision || doc.SHA256 == typed.BaseSHA256 {
		t.Fatalf("document = %+v", doc)
	}
	dirty, err := adapter.DirtyDocuments(ctx, p.ID, p.SourceBranch)
	testutil.FailErr(t, "dirty documents", err)
	if len(dirty) != 1 || dirty[0].Path != "a.txt" || dirty[0].RootID != rootID || dirty[0].Text != "typed\n" {
		t.Fatalf("dirty = %+v", dirty)
	}

	_, err = adapter.ApplyAgentEdit(ctx, p.ID, tools.EditorDocumentEdit{
		DocumentID: doc.ID, ExpectedRevision: doc.Revision - 1, Content: "stale\n", OperationID: uuid.NewString(),
	})
	if !errors.Is(err, tools.ErrEditorDocumentMoved) {
		t.Fatalf("stale apply = %v, want ErrEditorDocumentMoved", err)
	}
	applied, err := adapter.ApplyAgentEdit(ctx, p.ID, tools.EditorDocumentEdit{
		DocumentID: doc.ID, ExpectedRevision: doc.Revision, Content: "typed agent\n", OperationID: uuid.NewString(),
		SessionID: "chat", Turn: 2, ToolCallID: "call", ToolName: "edit",
	})
	testutil.FailErr(t, "apply", err)
	if !applied.Saved || applied.Document.Dirty || applied.Document.Text != "typed agent\n" || applied.Document.SHA256 == doc.SHA256 {
		t.Fatalf("applied = %+v", applied)
	}
	raw, err := os.ReadFile(filepath.Join(p.Roots[0].Path, "a.txt"))
	testutil.FailErr(t, "read disk", err)
	if string(raw) != "typed agent\n" {
		t.Fatalf("disk = %q", raw)
	}
}

func TestAgentBasisAdvancesOnlyForTextTheAgentProposed(t *testing.T) {
	adapter, service, p, rootID := newAdapterFixture(t)
	read, _, err := adapter.OpenDocument(t.Context(), p.ID, p.SourceBranch, rootID, "a.txt")
	testutil.FailErr(t, "read base", err)
	adapter.RememberAgentRead(p.ID, "chat-a", read)
	_, err = service.ReplaceSnapshot(t.Context(), read.ID, p.ID, editordoc.SnapshotReplacement{
		DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: read.Revision}, Content: "base\nhuman typing\n", EOL: "lf",
	})
	testutil.FailErr(t, "concurrent typing", err)
	basis, err := adapter.AgentReadBase(t.Context(), p.ID, "chat-a", read.ID, nil)
	testutil.FailErr(t, "agent basis", err)
	// The response's frozen set holds the same basis; a later write in the
	// response advances it only when the result is the agent's own text.
	frozen := tools.NewAgentReadBases(adapter.FreezeAgentReads(p.ID, "chat-a"))
	merge := tools.EditorDocumentEdit{DocumentID: basis.ID, ExpectedRevision: basis.Revision, SessionID: "chat-a", OperationID: uuid.NewString(), Content: "BASE\n"}
	applied, err := adapter.ApplyAgentEdit(t.Context(), p.ID, merge)
	testutil.FailErr(t, "merge proposal", err)
	if applied.Document.Text != "BASE\nhuman typing\n" {
		t.Fatalf("lost human text: %q", applied.Document.Text)
	}
	adapter.AdvanceAgentRead(p.ID, "chat-a", frozen, merge, applied.Document)
	if _, err := adapter.AgentReadBase(t.Context(), p.ID, "chat-a", read.ID, nil); !errors.Is(err, tools.ErrEditorReadRequired) {
		t.Fatalf("unseen merged text became a read: %v", err)
	}
	if _, err := adapter.AgentReadBase(t.Context(), p.ID, "chat-a", read.ID, frozen); !errors.Is(err, tools.ErrEditorReadRequired) {
		t.Fatalf("merged text stayed a basis for the same response: %v", err)
	}
	adapter.RememberAgentRead(p.ID, "chat-a", applied.Document)
	frozen = tools.NewAgentReadBases(adapter.FreezeAgentReads(p.ID, "chat-a"))
	own := tools.EditorDocumentEdit{DocumentID: read.ID, ExpectedRevision: applied.Document.Revision, SessionID: "chat-a", OperationID: uuid.NewString(), Content: "UPDATED\nhuman typing\n"}
	applied, err = adapter.ApplyAgentEdit(t.Context(), p.ID, own)
	testutil.FailErr(t, "edit known current text", err)
	adapter.AdvanceAgentRead(p.ID, "chat-a", frozen, own, applied.Document)
	basis, err = adapter.AgentReadBase(t.Context(), p.ID, "chat-a", read.ID, nil)
	testutil.FailErr(t, "advance known basis", err)
	if basis.Text != applied.Document.Text || basis.Revision != applied.Document.Revision {
		t.Fatalf("own result not retained: %+v", basis)
	}
	basis, err = adapter.AgentReadBase(t.Context(), p.ID, "chat-a", read.ID, frozen)
	testutil.FailErr(t, "advance frozen basis", err)
	if basis.Revision != applied.Document.Revision {
		t.Fatalf("the response cannot edit the text it just wrote: %+v", basis)
	}
}
