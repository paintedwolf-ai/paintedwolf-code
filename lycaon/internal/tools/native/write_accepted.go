package native

import (
	"context"
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// PublishAccepted applies the current write gates to an already accepted draft.
// The editor callback saves that revision without accepting another edit.
func (t *WriteTool) PublishAccepted(ctx context.Context, tc tools.ToolContext, memory *toolcontract.ProgramMemory, publication tools.EditorDocumentPublication) (tools.EditorDocumentApplied, error) {
	path, doc := publication.Path, publication.Document
	// The snapshot is retained by the caller. This phase admits disk decode,
	// normalized/EOL text, encoded save bytes and the save's second projection.
	if memory != nil {
		release, err := memory.Reserve(ctx, 2*publication.BaseBytes+6*int64(len(doc.Text))+4096)
		if err != nil {
			return tools.EditorDocumentApplied{}, err
		}
		defer release()
	}
	result := tools.EditorDocumentApplied{Document: doc}
	if err := assertProfileWriteScope(ctx, t.Boundary, tc, path, "write"); err != nil {
		return result, writeScopeReject(ctx, t.Boundary, path, tc.ProfileID(), "write", err)
	}
	if err := beforeWorkerMutation(ctx, tc, path); err != nil {
		return result, err
	}
	resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tc, path)
	if err != nil {
		return result, err
	}
	disk, err := sourceview.ReadTextDocument("read", sourceview.AccessRead, resolved, tc.Host.MaxToolSpillBytes)
	st := sourceview.Text{}
	var before *string
	if err == nil {
		st.Content, st.Disk = disk.Text(), &disk
		before = &st.Content
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	_, err = t.writeText(ctx, tc, resolved, st, before, WriteInput{Path: path, Content: doc.Text}, func(final string) (landing, error) {
		if final != doc.Text {
			return landing{}, tools.ErrEditorDocumentMoved
		}
		saved, err := publication.Publish(ctx)
		if err != nil {
			return landing{}, err
		}
		result.Document, result.Saved = saved, true
		return landing{editor: true, saved: true, documentID: doc.ID, acceptedRevision: doc.Revision, publishedSHA256: saved.BaseSHA256}, nil
	})
	return result, err
}
