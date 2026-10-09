package sourceview

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
	"github.com/lycaon/lycaon/pkg/api"
)

// The receipt note for text served from an unsaved document.
const EditorDraftNote = "served the shared editor document; its unsaved text has not been published as a file version"

// Text retains the origin needed to write back to the served document.
type Text struct {
	Content string
	// disk is the file's own document; nil when the editor served the text.
	Disk *textfile.UntrustedDocument
	// editor is the open document; nil when the file served the text.
	Editor *tools.EditorDocumentText
}

// SHA256 identifies the served bytes.
func (s Text) SHA256() string {
	if s.Editor != nil {
		return s.Editor.SHA256
	}
	if s.Disk != nil {
		return s.Disk.RawSHA256()
	}
	return ""
}

// ProjectDocuments returns the shared-document view when this invocation
// reads the project tree itself; a worker on a private branch reads a copy.
func ProjectDocuments(tctx tools.ToolContext) (tools.EditorDocuments, bool) {
	if tctx.Source.EditorDocuments == nil || strings.TrimSpace(tctx.Identity.ProjectID) == "" {
		return nil, false
	}
	if tctx.Source.SourceWorkspaceKind != api.SourceWorkspaceKindProject || strings.TrimSpace(tctx.Source.WorkerBranchRoot) != "" {
		return nil, false
	}
	return tctx.Source.EditorDocuments, true
}

// DocumentsFor narrows ProjectDocuments to one resolved path:
// granted paths outside the roots have no document.
func DocumentsFor(tctx tools.ToolContext, resolved projectpaths.Resolved) (tools.EditorDocuments, bool) {
	if resolved.External || strings.TrimSpace(resolved.Root.ID) == "" {
		return nil, false
	}
	return ProjectDocuments(tctx)
}

// LoadText serves the accepted document when one exists; a failed lookup
// cannot authorize a filesystem write that would bypass an unknown draft.
// The document is held to the file's access budget.
func LoadText(ctx context.Context, tool string, access Access, tctx tools.ToolContext, resolved projectpaths.Resolved) (Text, error) {
	if documents, ok := DocumentsFor(tctx, resolved); ok {
		branch, err := tctx.SourceBranch(resolved.Root.ID)
		if err != nil {
			return Text{}, err
		}
		doc, open, err := documents.OpenDocument(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, resolved.ScopeRel)
		if err != nil {
			return Text{}, fmt.Errorf("read collaborative document: %w", err)
		}
		if open {
			if size := int64(len(doc.Text)); size > access.MaxBytes() {
				return Text{}, access.SizeReject(tool, resolved.DisplayPath, size, access.MaxBytes())
			}
			return Text{Content: doc.Text, Editor: &doc}, nil
		}
	}
	disk, err := ReadTextDocument(tool, access, resolved, tctx.Host.MaxToolSpillBytes)
	if err != nil {
		return Text{}, err
	}
	return Text{Content: disk.Text(), Disk: &disk}, nil
}

// Unsaved editor text has no recorded source version.
func Stamp(ctx context.Context, tctx tools.ToolContext, absPath string, st Text) *surveyreceipt.SourceContext {
	stamp := ReadStamp(ctx, tctx, absPath, st.SHA256())
	if st.Editor == nil {
		return stamp
	}
	if stamp == nil {
		stamp = &surveyreceipt.SourceContext{SHA256Short: ShortSHA(st.SHA256())}
	}
	stamp.Editor = &surveyreceipt.EditorContext{Revision: st.Editor.Revision, Dirty: st.Editor.Dirty, Diverged: st.Editor.Diverged, Absent: st.Editor.Absent}
	if st.Editor.Dirty {
		stamp.Recorded = false
		stamp.VersionID = ""
		stamp.Note = EditorDraftNote
	}
	if st.Editor.Absent {
		stamp.Note = EditorAbsentNote
	}
	return stamp
}

// The receipt note for a draft whose file no longer exists on disk.
const EditorAbsentNote = "served the shared editor document; the file was deleted on disk and only this unsaved draft remains, so a write recreates it"

// DraftOverlay maps absolute paths to the unsaved text the person has
// open for them, so a search over the tree reads what they see.
type DraftOverlay map[string]string

// DraftsFor loads the project's unsaved documents for one invocation.
// Empty when the invocation is not reading the project tree.
func DraftsFor(ctx context.Context, tctx tools.ToolContext) DraftOverlay {
	documents, ok := ProjectDocuments(tctx)
	if !ok {
		return nil
	}
	dirty, err := documents.DirtyDocuments(ctx, tctx.Identity.ProjectID, tctx.Source.ProjectSourceBranch)
	if err != nil {
		slog.WarnContext(ctx, "editor documents unavailable for search; searching files", "err", err)
		return nil
	}
	if len(dirty) == 0 {
		return nil
	}
	overlay := make(DraftOverlay, len(dirty))
	for _, d := range dirty {
		abs, root, err := projectroot.ResolveAbs(tctx.Source.Roots, d.RootID, d.Path)
		if err != nil || root.ID != d.RootID {
			continue
		}
		overlay[abs] = d.Text
	}
	return overlay
}

// lookup returns the unsaved text for an absolute path when the person has it open.
func (o DraftOverlay) Lookup(abs string) ([]byte, bool) {
	if len(o) == 0 {
		return nil, false
	}
	text, ok := o[abs]
	if !ok {
		return nil, false
	}
	return []byte(text), true
}
