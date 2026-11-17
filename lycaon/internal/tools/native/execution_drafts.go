package native

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// SaveDrafts saves the person's unsaved text at path, or in the subtree
// beneath it, before an operation acts on the file: an agent touching a file
// autosaves it. A document whose file changed outside it is a revision
// conflict, since saving would overwrite that change.
func (e ExecutionRevisions) SaveDrafts(ctx context.Context, tctx tools.ToolContext, path string, descendants bool) error {
	// The save is the person's text reaching its own file; the operation's
	// write authority is checked where it binds its paths.
	resolved, err := projectpaths.ResolveRead(ctx, e.Boundary, tctx, path)
	if err != nil {
		return err
	}
	documents, ok := sourceview.DocumentsFor(tctx, resolved)
	if !ok {
		return nil
	}
	branch, err := tctx.SourceBranch(resolved.Root.ID)
	if err != nil {
		return err
	}
	queryPath := resolved.ScopeRel
	if queryPath == "." {
		queryPath = ""
	}
	err = documents.SaveForAgentOperation(ctx, tctx.Identity.ProjectID, branch, resolved.Root.ID, queryPath, descendants, tools.EditorAgentCall{
		SessionID: tctx.Identity.SessionID, Turn: tctx.Identity.UserTurn, ToolCallID: tctx.Identity.ToolCallID, ToolName: "command",
	})
	var changed *tools.EditorFileChanged
	if errors.As(err, &changed) {
		return fileChangedConflict(resolved, changed)
	}
	return err
}

// captureDocument pins the person's unsaved text at a path as the bytes a save
// would write; ok is false when the path holds no unsaved text.
func (e ExecutionRevisions) captureDocument(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, maxBytes int64) (data []byte, revision toolcontract.ExecutionRevision, release func(), ok bool, err error) {
	unsaved, err := sourceview.HoldsUnsavedText(ctx, tctx, resolved)
	if err != nil || !unsaved {
		return nil, revision, nil, false, err
	}
	doc, open, err := sourceview.OpenDocument(ctx, tctx, resolved)
	if err != nil || !open || !doc.Dirty {
		return nil, revision, nil, false, err
	}
	encoded, err := textfile.EncodeDocument(doc.Text, doc.EOL, doc.Encoding, textfile.LimitsForRaw(maxBytes))
	if errors.Is(err, textfile.ErrRawTooLarge) || errors.Is(err, textfile.ErrTextTooLarge) {
		return nil, revision, nil, false, sourceview.AccessRead.SizeReject("read", resolved.DisplayPath, int64(len(doc.Text)), maxBytes)
	}
	if err != nil {
		return nil, revision, nil, false, err
	}
	release, err = e.reserveCapture(ctx, int64(len(encoded)))
	if err != nil {
		return nil, revision, nil, false, err
	}
	root, rel := revisionLocation(resolved)
	digest := textfile.SHA256(encoded)
	revision = toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.AcceptedDocumentRevision, Basis: toolcontract.CapturedRevision,
		RootID: root, Path: rel, DocumentID: doc.ID, DocumentRevision: doc.Revision, DiskSHA256: doc.BaseSHA256,
		Representation: toolcontract.OriginalEncodedBytes, ContentSHA256: digest, ContentBytes: int64(len(encoded))}
	if err := revision.Validate(); err != nil {
		release()
		return nil, revision, nil, false, err
	}
	return encoded, revision, release, true, nil
}

// divergedConflict reports a destination whose document holds a draft over a
// file changed outside it: the base the draft sits on against the file now on disk.
func divergedConflict(resolved projectpaths.Resolved, revision toolcontract.ExecutionRevision, doc *tools.EditorDocumentText) *ExecutionRevisionConflict {
	changed := &tools.EditorFileChanged{RootID: resolved.Root.ID, Path: revision.Path, BaseSHA256: doc.BaseSHA256, BaseAbsent: doc.BaseSHA256 == "", DiskAbsent: true}
	if _, disk, err := captureRawRevision(resolved, revision); err == nil {
		changed.DiskSHA256, changed.DiskAbsent = disk.DiskSHA256, false
	}
	conflict := fileChangedConflict(resolved, changed)
	conflict.Path = resolved.DisplayPath
	return conflict
}

// fileChangedConflict states the base a document sits on and the file now on
// disk as the destination's pinned and current revisions.
func fileChangedConflict(resolved projectpaths.Resolved, changed *tools.EditorFileChanged) *ExecutionRevisionConflict {
	root, _ := revisionLocation(resolved)
	revision := func(sha string, absent bool) toolcontract.ExecutionRevision {
		r := toolcontract.ExecutionRevision{Role: toolcontract.DestinationRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision,
			RootID: root, Path: changed.Path, DiskSHA256: sha, Representation: toolcontract.OriginalEncodedBytes, ContentSHA256: sha}
		if absent {
			r.Mode, r.DiskSHA256, r.ContentSHA256 = toolcontract.AbsentRevision, "", ""
		}
		return r
	}
	return &ExecutionRevisionConflict{Path: changed.Path, Pinned: revision(changed.BaseSHA256, changed.BaseAbsent), Current: revision(changed.DiskSHA256, changed.DiskAbsent)}
}
