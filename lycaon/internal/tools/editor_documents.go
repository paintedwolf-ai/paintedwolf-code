package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
)

// ErrEditorDocumentMoved reports that the open document's revision advanced
// since the agent's read or a prepared review. Retrying retains the same
// read basis; incompatible edits require another agent read.
var ErrEditorDocumentMoved = errors.New("editor document changed since it was read")

var ErrEditorReadRequired = errors.New("read the shared document before editing it")

// ErrEditorExecutionConflict reports that a pinned program edit's document or
// published file moved after capture. It is never retried or rebased.
var ErrEditorExecutionConflict = errors.New("editor document changed since the program captured it")

// EditorDocumentText is what an open editor document presents for one
// project-tree path: the text the person sees, whatever the file holds.
type EditorDocumentText struct {
	ID       string
	Revision int64
	// Text is the LF-normalized draft.
	Text string
	// SHA256 identifies the bytes a save of this draft writes; for a clean
	// document it is the saved file's hash.
	SHA256 string
	// BaseSHA256 is the published file the document sits on; empty when absent.
	BaseSHA256 string
	// Encoding and EOL are the recorded representation a save writes.
	Encoding, EOL string
	Dirty         bool
	Diverged      bool
	// Absent says the path has no file on disk; the document holds a draft
	// for it, and a write recreates the file.
	Absent bool
	// ReadSpans restricts a partial read basis to complete delivered lines.
	// ReadWhole distinguishes a complete read from an empty partial scope.
	ReadSpans []documentcore.LineSpan
	ReadWhole bool
}

// CoversLines reports whether every line in the inclusive range was delivered.
func (d EditorDocumentText) CoversLines(startLine, endLine int) bool {
	if d.ReadWhole {
		return true
	}
	if startLine <= 0 || endLine < startLine {
		return false
	}
	for _, s := range d.ReadSpans {
		if s.StartLine <= startLine && endLine <= s.EndLine {
			return true
		}
	}
	return false
}

// EditorDocumentState is accepted metadata; it never materializes draft text.
type EditorDocumentState struct {
	ID, RootID, Path, BaseSHA256 string
	Revision                     int64
	DraftBytes, BaseBytes        int64
	Absent                       bool
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
	// Pin admits a program edit only at its captured state: no read basis,
	// anchoring or retry. ExpectedRevision equals Pin.Revision.
	Pin *EditorDocumentPin
}

// EditorDocumentPin is the accepted revision and published base a program
// operation captured before preparing its change.
type EditorDocumentPin struct {
	Revision   int64
	BaseSHA256 string
	Absent     bool
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

type EditorDocumentPublicationCandidate struct {
	DraftBytes, BaseBytes int64
	Load                  func(context.Context) (EditorDocumentPublication, error)
}

type EditorDocumentPublication struct {
	BaseBytes    int64
	RootID, Path string
	Document     EditorDocumentText
	Publish      func(context.Context) (EditorDocumentText, error)
}

type EditorDocuments interface {
	FreezeAgentReads(projectID, sessionID string) map[string]documentcore.ReadBasis
	RememberAgentRead(projectID, sessionID string, document EditorDocumentText)
	RememberAgentReadSpans(projectID, sessionID string, document EditorDocumentText, spans []documentcore.LineSpan)
	// AdvanceAgentRead moves the basis to a result that is exactly the proposed
	// text, in the chat's cache and this response's frozen set, and withdraws
	// it for any other result.
	AdvanceAgentRead(projectID, sessionID string, frozen *AgentReadBases, edit EditorDocumentEdit, document EditorDocumentText)
	AgentReadBase(ctx context.Context, projectID, sessionID, documentID string, frozen *AgentReadBases) (EditorDocumentText, error)
	PreviewAgentEdits(ctx context.Context, projectID string, edits []EditorDocumentEdit) ([]EditorDocumentPreview, error)
	// OpenDocument opens existing supported text, including files without a UI tab.
	// Missing and unsupported files return ok false.
	OpenDocument(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string) (doc EditorDocumentText, ok bool, err error)
	// VisitUnsavedDocuments streams accepted metadata for an exact path or subtree.
	// Empty path selects the root; no document is opened or reconciled.
	VisitUnsavedDocuments(ctx context.Context, projectID string, branch sourcebranch.ID, rootID, path string, descendants bool, visit func(EditorDocumentState) error) error
	// DirtyDocuments lists the project's documents whose draft differs from
	// the saved file, so a search reads the person's text instead.
	DirtyDocuments(ctx context.Context, projectID string, branch sourcebranch.ID) ([]EditorDocumentPath, error)
	// VisitAgentPublications offers accepted drafts; the host admits each save.
	VisitAgentPublications(ctx context.Context, projectID string, branch sourcebranch.ID, sessionID string, turn int, toolCallID string, visit func(EditorDocumentPublicationCandidate) error) error
	// ApplyAgentEdit anchors changes in ExpectedRevision and publishes them.
	// Overlapping changes return ErrEditorDocumentMoved without changing the
	// read basis; a failed publication of an accepted edit is a result.
	ApplyAgentEdit(ctx context.Context, projectID string, edit EditorDocumentEdit) (EditorDocumentApplied, error)
	// ApplyAgentEdits commits a validated document set before publishing individual files.
	ApplyAgentEdits(ctx context.Context, projectID string, edits []EditorDocumentEdit) ([]EditorDocumentApplied, error)
}

// ProjectDocuments returns the shared document view for this project's own tree.
// A worker on a private branch reads its separate copy.
func ProjectDocuments(tc ToolContext) (EditorDocuments, bool) {
	if tc.Source.EditorDocuments == nil || strings.TrimSpace(tc.Identity.ProjectID) == "" {
		return nil, false
	}
	if tc.Source.SourceWorkspaceKind != api.SourceWorkspaceKindProject || strings.TrimSpace(tc.Source.WorkerBranchRoot) != "" {
		return nil, false
	}
	return tc.Source.EditorDocuments, true
}
