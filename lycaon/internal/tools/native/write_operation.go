package native

import (
	"context"
	"fmt"
	"io"
	"math"
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
	// Memory is the invoking program's existing admission; conversational writes omit it.
	Memory *toolcontract.ProgramMemory
	// Exact publishes Bytes verbatim instead of encoding Content: a program's
	// redirect output, text or not. It requires the execution-pinned admission.
	Exact bool
	Bytes []byte
	// Source borrows an exact raw stream for this call; SourceBytes is its length.
	Source      io.Reader
	SourceBytes int64
	// Mode is the permission an exact pinned write gives a file it creates;
	// zero uses the ordinary write mode. A replaced file keeps its own.
	Mode os.FileMode
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
func (t *WriteTool) Apply(ctx context.Context, input WriteInput, tctx tools.ToolContext) (result WriteResult, err error) {
	if input.Source != nil && (!input.Exact || input.Admission != WriteExecutionPinned) {
		return WriteResult{}, unsupportedWriteAdmission()
	}
	if input.Admission != WriteConversational && input.Admission != WriteExecutionPinned {
		return WriteResult{}, unsupportedWriteAdmission()
	}
	if input.Exact && ((input.Source != nil && (input.Bytes != nil || input.SourceBytes < 0 || input.SourceBytes == math.MaxInt64)) || (input.Source == nil && input.SourceBytes != 0)) {
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
		defer func() {
			if err == nil && result.Accepted && !result.Saved {
				err = &UnpublishedWrite{Result: result}
			}
		}()

		if input.Exact {
			return t.applyPinnedBytes(ctx, input, tctx, resolved)
		}
		return t.applyPinned(ctx, input, tctx, resolved)
	}
	if input.Exact {
		return WriteResult{}, unsupportedWriteAdmission()
	}
	return retryEditorOperation(ctx, resolved.DisplayPath, func() (WriteResult, error) {
		// A missing file is a create; otherwise the base is the served source text.
		var before *string
		st, err := loadAgentSourceText(ctx, "write", tctx, resolved)
		if err == nil {
			beforeText := st.Content
			before = &beforeText
		} else if !os.IsNotExist(err) {
			if toolrejection.AsToolReject(err) != nil {
				return WriteResult{}, err
			}
			return WriteResult{}, fmt.Errorf("read failed: %w", err)
		}
		return t.writeText(ctx, tctx, resolved, st, before, input, func(final string) (landing, error) {
			authored := input.Content
			if input.Append && before != nil {
				authored = *before + input.Content
			}
			return landEditedText(ctx, tctx, "write", resolved, st, editorTextProposal{Authored: authored, Reviewed: final})
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
