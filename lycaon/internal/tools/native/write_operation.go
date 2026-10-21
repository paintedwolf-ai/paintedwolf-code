package native

import (
	"context"
	"fmt"
	"os"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// WriteInput is shared by retained structured write and native command adapters.
type WriteInput struct {
	Path      string
	Content   string
	Append    bool
	Admission WriteAdmission
	// Destination and Sources are the execution-pinned admission's captures:
	// the destination revision or absence, and the inputs Content came from.
	Destination toolcontract.ExecutionRevision
	Sources     []toolcontract.ExecutionRevision
}

type WriteAdmission uint8

const (
	// WriteConversational edits from the chat's delivered read and rebases
	// compatible concurrent changes.
	WriteConversational WriteAdmission = iota
	// WriteExecutionPinned publishes a program's prepared bytes only at the
	// captured revisions; any movement conflicts without a retry.
	WriteExecutionPinned
)

// WriteResult distinguishes accepted document edits from saved publication.
type WriteResult struct {
	Path             string
	Appended         bool
	InputBytes       int
	ResultBytes      int
	Accepted         bool
	Saved            bool
	HeldVersionID    string
	PublicationError error
	Note             string
	// DocumentID and AcceptedRevision identify a pinned write's accepted
	// document revision; PublishedSHA256 hashes the file once it holds it.
	DocumentID       string
	AcceptedRevision int64
	PublishedSHA256  string
}

// Apply performs the complete native write operation, including scope, review,
// revision, syntax, attribution and the existing source write door.
func (t *WriteTool) Apply(ctx context.Context, input WriteInput, tctx tools.ToolContext) (WriteResult, error) {
	if input.Admission != WriteConversational && input.Admission != WriteExecutionPinned {
		return WriteResult{}, unsupportedWriteAdmission()
	}
	path := input.Path
	if path == "" {
		return WriteResult{}, toolkit.MissingArg("path")
	}
	if err := assertProfileWriteScope(ctx, t.Boundary, tctx, path, "write"); err != nil {
		return WriteResult{}, writeScopeReject(ctx, t.Boundary, path, tctx.ProfileID(), "write", err)
	}
	if err := beforeWorkerMutation(ctx, tctx, path); err != nil {
		return WriteResult{}, err
	}
	resolved, err := projectpaths.ResolveWrite(ctx, t.Boundary, tctx, path)
	if err != nil {
		return WriteResult{}, err
	}
	if input.Admission == WriteExecutionPinned {
		return t.applyPinned(ctx, input, tctx, resolved)
	}
	return retryEditorOperation(ctx, resolved.DisplayPath, func() (WriteResult, error) {
		// A missing file is a create; otherwise the base is the served source text.
		var before *string
		st, err := loadAgentSourceText(ctx, "write", tctx, resolved)
		if err == nil {
			beforeText := st.Content
			before = &beforeText
		} else if !os.IsNotExist(err) {
			return WriteResult{}, fmt.Errorf("read failed: %w", err)
		}
		return t.writeText(ctx, tctx, resolved, st, before, input, func(final string) (landing, error) {
			return landEditedText(ctx, tctx, "write", resolved, st, final)
		})
	})
}

// writeText runs the gates both admissions share, then lands the final text.
func (t *WriteTool) writeText(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, st sourceview.Text, before *string, input WriteInput, land func(string) (landing, error)) (WriteResult, error) {
	path := resolved.DisplayPath
	proposed := input.Content
	if input.Append {
		if before == nil {
			return WriteResult{}, sourceview.PathNotFound("write", path, resolved.Abs)
		}
		proposed = *before + input.Content
	}
	if err := guardMutationContent("write", path, proposed); err != nil {
		return WriteResult{}, err
	}
	finalContent := proposed
	reportTextIntent(tctx, resolved, st, before != nil, proposed)
	if t.ContentApply != nil {
		var err error
		finalContent, err = t.ContentApply.GateApply(ctx, "write", path, before, proposed, tctx)
		if err != nil {
			return WriteResult{}, err
		}
		if finalContent != proposed {
			reportTextIntent(tctx, resolved, st, before != nil, finalContent)
		}
	}
	if err := rejectIfSyntaxUnhealthy(ctx, "write", path, before, finalContent, mutationSeam{}); err != nil {
		return WriteResult{}, err
	}
	landed, err := land(finalContent)
	if err != nil {
		return WriteResult{}, err
	}
	captureFileEdit(tctx, path, finalContent, before)
	afterSuccessfulMutation(ctx, tctx, path)
	return WriteResult{Path: path, Appended: input.Append, InputBytes: len(input.Content), ResultBytes: len(finalContent),
		Accepted: true, Saved: !landed.editor || landed.saved, HeldVersionID: landed.heldVersionID,
		PublicationError: landed.publicationError, Note: landed.note(),
		DocumentID: landed.documentID, AcceptedRevision: landed.acceptedRevision, PublishedSHA256: landed.publishedSHA256}, nil
}

func unsupportedWriteAdmission() error {
	return &toolrejection.ToolReject{Code: "COMMAND_NATIVE_FORM_UNSUPPORTED", Data: map[string]any{"operation": "write", "admission": "unknown"}}
}

// SettlementRevisions are the accepted and published revisions a program
// receipt records for this write; published is empty until the file holds it.
func (result WriteResult) SettlementRevisions() (accepted, published string) {
	if result.PublishedSHA256 != "" {
		published = "sha256:" + result.PublishedSHA256
	}
	accepted = published
	if result.DocumentID != "" {
		accepted = fmt.Sprintf("document:%s@%d", result.DocumentID, result.AcceptedRevision)
	}
	return accepted, published
}

func (result WriteResult) Receipt() string {
	var receipt string
	if result.Appended {
		receipt = fmt.Sprintf("Appended %d bytes to %s (file now %d bytes)", result.InputBytes, result.Path, result.ResultBytes)
	} else {
		receipt = fmt.Sprintf("Wrote %d bytes to %s", result.ResultBytes, result.Path)
	}
	if result.Note != "" {
		receipt += "\n" + result.Note
	}
	return receipt
}
