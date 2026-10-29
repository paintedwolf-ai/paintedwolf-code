package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"

	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// ExecutionBytes is one pinned literal input: the exact stream bytes a native
// byte utility consumes and the revision naming them. Binary content is data.
type ExecutionBytes struct {
	Path     string
	Bytes    []byte
	Revision toolcontract.ExecutionRevision
}

// CaptureBytes pins one literal byte input before the utility reads it. The
// accepted document is encoded in its recorded representation; otherwise the
// published file bytes are captured. Inputs above maxBytes refuse with
// READ_FILE_TOO_LARGE before materialization; nothing is truncated.
func (e ExecutionRevisions) CaptureBytes(ctx context.Context, tctx tools.ToolContext, path string, maxBytes int64) (ExecutionBytes, error) {
	resolved, err := projectpaths.ResolveRead(ctx, e.Boundary, tctx, path)
	if err != nil {
		return ExecutionBytes{}, err
	}
	content, revision, err := captureBytes(ctx, tctx, resolved, maxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return ExecutionBytes{}, sourceview.PathNotFound("read", resolved.DisplayPath, resolved.Abs)
	}
	if err != nil {
		return ExecutionBytes{}, err
	}
	return ExecutionBytes{Path: resolved.DisplayPath, Bytes: content, Revision: revision}, nil
}

func captureBytes(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, maxBytes int64) ([]byte, toolcontract.ExecutionRevision, error) {
	root, rel := revisionLocation(resolved)
	revision := toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Basis: toolcontract.CapturedRevision, RootID: root, Path: rel, Representation: toolcontract.OriginalEncodedBytes}
	doc, open, err := sourceview.OpenDocument(ctx, tctx, resolved)
	if err != nil {
		return nil, revision, err
	}
	var content []byte
	if open {
		if content, err = sourceview.DocumentBytes(doc, resolved, maxBytes); err != nil {
			return nil, revision, err
		}
		revision.Mode, revision.DocumentID, revision.DocumentRevision, revision.DiskSHA256 = toolcontract.AcceptedDocumentRevision, doc.ID, doc.Revision, doc.BaseSHA256
		revision.ContentSHA256 = textfile.SHA256(content)
	} else {
		content, err = sourceview.ReadContentCapped("read", sourceview.AccessRead, resolved.EffectLocation(), resolved.DisplayPath, resolved.Compressed, maxBytes)
		if os.IsNotExist(err) {
			return nil, revision, fs.ErrNotExist
		}
		if err != nil {
			return nil, revision, err
		}
		revision.Mode = toolcontract.PublishedDiskRevision
		revision.DiskSHA256 = textfile.SHA256(content)
		revision.ContentSHA256 = revision.DiskSHA256
	}
	revision.ContentBytes = int64(len(content))
	return content, revision, revision.Validate()
}

// recaptureBytes reads a literal pin's current state for Verify.
func recaptureBytes(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved) (toolcontract.ExecutionRevision, error) {
	_, revision, err := captureBytes(ctx, tctx, resolved, readcaps.MaxFileBytes)
	return revision, err
}

// PinOpened identifies the published bytes of a file another owner has just
// opened, hashing through the held descriptor without moving its offset, so
// the pin names exactly the object that owner reads.
func (e ExecutionRevisions) PinOpened(resolved projectpaths.Resolved, file *os.File) (toolcontract.ExecutionRevision, bool, error) {
	info, err := file.Stat()
	if err != nil {
		return toolcontract.ExecutionRevision{}, false, err
	}
	if !info.Mode().IsRegular() {
		return toolcontract.ExecutionRevision{}, false, nil
	}
	h := sha256.New()
	size, err := io.Copy(h, io.NewSectionReader(file, 0, info.Size()))
	if err != nil {
		return toolcontract.ExecutionRevision{}, false, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	root, rel := revisionLocation(resolved)
	revision := toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision,
		RootID: root, Path: rel, DiskSHA256: digest, Representation: toolcontract.OriginalEncodedBytes, ContentSHA256: digest, ContentBytes: size}
	return revision, true, revision.Validate()
}
