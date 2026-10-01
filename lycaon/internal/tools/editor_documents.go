package tools

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

// ErrEditorDocumentMoved reports that the open document's revision advanced
// since the agent's read or a prepared review. Retrying retains the same
// read basis; incompatible edits require another agent read.
var ErrEditorDocumentMoved = errors.New("editor document changed since it was read")

var ErrEditorReadRequired = errors.New("read the shared document before editing it")

// EditorDocumentText is what an open editor document presents for one
// project-tree path: the text the person sees, whatever the file holds.
type EditorDocumentText struct {
	ID       string
	Revision int64
	// Text is the LF-normalized draft.
	Text string
	// SHA256 identifies the bytes a save of this draft writes; for a clean
	// document it is the saved file's hash.
	SHA256   string
	Dirty    bool
	Diverged bool
	// Absent says the path has no file on disk; the document holds a draft
	// for it, and a write recreates the file.
	Absent bool
}

// EditorDocumentPath addresses one open document in a project tree.
type EditorDocumentPath struct {
	RootID string
	Path   string
	Text   string
}

// EditorDocumentEdit is one agent edit applied through an open document.
// ExpectedRevision is the revision the content was computed against.
type EditorDocumentEdit struct {
	DocumentID       string
	ExpectedRevision int64
	ReviewedRevision int64
	Content          string
	OperationID      string
	SessionID        string
	Turn             int
	ToolCallID       string
	ToolName         string
}

// EditorDocumentApplied reports where an accepted agent edit landed: Saved
// says the file holds it, PublicationError says why a publication failed.
type EditorDocumentApplied struct {
	PublicationError error
	Document         EditorDocumentText
	Saved            bool
	// HeldVersionID addresses the retained state holding an unsaved edit.
	HeldVersionID string
}

// EditorDocumentPreview describes the exact rebased text awaiting acceptance.
type EditorDocumentPreview struct {
	Before                  EditorDocumentText
	After, AfterSHA256      string
	BeforeBytes, AfterBytes int64
}

type EditorDocuments interface {
	FreezeAgentReads(projectID, sessionID string) map[string]int64
	RememberAgentRead(projectID, sessionID string, document EditorDocumentText)
	// AdvanceAgentRead moves the basis to a result that is exactly the proposed
	// text, in the chat's cache and this response's frozen set, and withdraws
	// it for any other result.
	AdvanceAgentRead(projectID, sessionID string, frozen *AgentReadBases, edit EditorDocumentEdit, document EditorDocumentText)
	AgentReadBase(ctx context.Context, projectID, sessionID, documentID string, frozen *AgentReadBases) (EditorDocumentText, error)
	PreviewAgentEdits(ctx context.Context, projectID string, edits []EditorDocumentEdit) ([]EditorDocumentPreview, error)
	// OpenDocument opens existing supported text, including files without a UI tab.
	// Missing and unsupported files return ok false.
	OpenDocument(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (doc EditorDocumentText, ok bool, err error)
	// DirtyDocuments lists the project's documents whose draft differs from
	// the saved file, so a search reads the person's text instead.
	DirtyDocuments(ctx context.Context, projectID string, branch sourcebranch.ID) ([]EditorDocumentPath, error)
	// ApplyAgentEdit anchors changes in ExpectedRevision and publishes them.
	// Overlapping changes return ErrEditorDocumentMoved without changing the
	// read basis; a failed publication of an accepted edit is a result.
	ApplyAgentEdit(ctx context.Context, projectID string, edit EditorDocumentEdit) (EditorDocumentApplied, error)
	// ApplyAgentEdits commits a validated document set before publishing individual files.
	ApplyAgentEdits(ctx context.Context, projectID string, edits []EditorDocumentEdit) ([]EditorDocumentApplied, error)
}
