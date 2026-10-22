package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
)

// ExecutionStream is one pinned input opened for streaming without loading the
// entire file into memory when backed by an uncompressed regular file on disk.
type ExecutionStream struct {
	Path            string
	Reader          io.ReadCloser
	Revision        toolcontract.ExecutionRevision
	FirstNullOffset int
}

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
	revision, _, ok, err := e.pinOpened(resolved, file)
	return revision, ok, err
}

func (e ExecutionRevisions) pinOpened(resolved projectpaths.Resolved, file *os.File) (toolcontract.ExecutionRevision, int, bool, error) {
	info, err := file.Stat()
	if err != nil {
		return toolcontract.ExecutionRevision{}, -1, false, err
	}
	if !info.Mode().IsRegular() {
		return toolcontract.ExecutionRevision{}, -1, false, nil
	}
	h := sha256.New()
	sr := io.NewSectionReader(file, 0, info.Size())
	buf := make([]byte, 32*1024)
	var size int64
	firstNull := -1
	for {
		n, rerr := sr.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			_, _ = h.Write(chunk)
			if firstNull < 0 {
				if idx := bytes.IndexByte(chunk, 0); idx >= 0 {
					firstNull = int(size) + idx
				}
			}
			size += int64(n)
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return toolcontract.ExecutionRevision{}, -1, false, rerr
		}
	}
	digest := hex.EncodeToString(h.Sum(nil))
	root, rel := revisionLocation(resolved)
	revision := toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision,
		RootID: root, Path: rel, DiskSHA256: digest, Representation: toolcontract.OriginalEncodedBytes, ContentSHA256: digest, ContentBytes: size}
	return revision, firstNull, true, revision.Validate()
}

// OpenStream pins one input before streaming its bytes. Open editor drafts,
// compressed files and credential files materialize through CaptureBytes;
// regular disk files stream through their pinned descriptor.
func (e ExecutionRevisions) OpenStream(ctx context.Context, tctx tools.ToolContext, path string, maxBytes int64) (ExecutionStream, error) {
	resolved, err := projectpaths.ResolveRead(ctx, e.Boundary, tctx, path)
	if err != nil {
		return ExecutionStream{}, err
	}
	_, open, err := sourceview.OpenDocument(ctx, tctx, resolved)
	if err != nil {
		return ExecutionStream{}, err
	}
	classified := resolved.Abs
	if _, rel, ok := tctx.SourceLocation(resolved.Abs); ok {
		classified = rel
	}
	if open || resolved.Compressed || (tctx.Effects.CredentialFiles != nil && protectedpath.IsCredentialFile(classified)) {
		return e.captureStream(ctx, tctx, resolved, maxBytes)
	}
	file, err := fseffect.OpenRead(resolved.EffectLocation())
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, fs.ErrNotExist) {
			return ExecutionStream{}, sourceview.PathNotFound("read", resolved.DisplayPath, resolved.Abs)
		}
		return ExecutionStream{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return ExecutionStream{}, err
	}
	if info.IsDir() {
		_ = file.Close()
		return ExecutionStream{}, &toolrejection.ToolReject{Code: "READ_IS_DIRECTORY", Data: map[string]any{"path": resolved.DisplayPath}}
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return e.captureStream(ctx, tctx, resolved, maxBytes)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		_ = file.Close()
		return ExecutionStream{}, sourceview.AccessRead.SizeReject("read", resolved.DisplayPath, info.Size(), maxBytes)
	}
	revision, firstNull, _, err := e.pinOpened(resolved, file)
	if err != nil {
		_ = file.Close()
		return ExecutionStream{}, err
	}
	return ExecutionStream{
		Path:            resolved.DisplayPath,
		Reader:          &sectionReadCloser{SectionReader: io.NewSectionReader(file, 0, revision.ContentBytes), closer: file},
		Revision:        revision,
		FirstNullOffset: firstNull,
	}, nil
}

func (e ExecutionRevisions) captureStream(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, maxBytes int64) (ExecutionStream, error) {
	if maxBytes <= 0 {
		maxBytes = readcaps.MaxFileBytes
	}
	content, revision, err := captureBytes(ctx, tctx, resolved, maxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return ExecutionStream{}, sourceview.PathNotFound("read", resolved.DisplayPath, resolved.Abs)
	}
	if err != nil {
		return ExecutionStream{}, err
	}
	if err := tctx.ObserveCredentialRead(ctx, resolved.Abs, resolved.DisplayPath, string(content)); err != nil {
		return ExecutionStream{}, fmt.Errorf("record credential file read: %w", err)
	}
	return ExecutionStream{
		Path:            resolved.DisplayPath,
		Reader:          io.NopCloser(bytes.NewReader(content)),
		Revision:        revision,
		FirstNullOffset: bytes.IndexByte(content, 0),
	}, nil
}

type sectionReadCloser struct {
	*io.SectionReader
	closer io.Closer
}

func (s *sectionReadCloser) Close() error {
	if s.closer == nil {
		return nil
	}
	err := s.closer.Close()
	s.closer = nil
	return err
}
