package native

import (
	"bytes"
	"context"
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
)

// ExecutionStream is one pinned input opened for streaming without loading the
// entire file into memory when backed by an uncompressed regular file on disk.
type ExecutionStream struct {
	Path            string
	Reader          io.ReadCloser
	Revision        toolcontract.ExecutionRevision
	FirstNullOffset int
	ContentLearned  bool
}

// ExecutionBytes is one pinned literal input: the exact stream bytes a native
// byte utility consumes and the revision naming them. Binary content is data.
type ExecutionBytes struct {
	Path     string
	Bytes    []byte
	Revision toolcontract.ExecutionRevision
	release  func()
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
	content, revision, release, err := e.captureBytes(ctx, tctx, resolved, maxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return ExecutionBytes{}, sourceview.PathNotFound("read", resolved.DisplayPath, resolved.Abs)
	}
	if err != nil {
		return ExecutionBytes{}, err
	}
	return ExecutionBytes{Path: resolved.DisplayPath, Bytes: content, Revision: revision, release: release}, nil
}

func (e ExecutionRevisions) captureBytes(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, maxBytes int64) ([]byte, toolcontract.ExecutionRevision, func(), error) {
	root, rel := revisionLocation(resolved)
	revision := toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision, RootID: root, Path: rel, Representation: toolcontract.OriginalEncodedBytes}
	if err := tools.ObserveCommandFileRead(ctx, resolved.Root.ID, resolved.ScopeRel); err != nil {
		return nil, revision, nil, err
	}
	content, release, err := e.readCapturedBytes(ctx, resolved, maxBytes)
	if err != nil {
		return nil, revision, nil, err
	}
	revision.DiskSHA256 = textfile.SHA256(content)
	revision.ContentSHA256, revision.ContentBytes = revision.DiskSHA256, int64(len(content))
	if err := revision.Validate(); err != nil {
		release()
		return nil, revision, nil, err
	}
	return content, revision, release, nil
}

// recaptureBytes reads a literal pin's current state for Verify.
func (e ExecutionRevisions) recaptureBytes(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved) (toolcontract.ExecutionRevision, error) {
	stream, err := e.OpenStream(ctx, tctx, resolved.Abs, 0)
	if err != nil {
		return toolcontract.ExecutionRevision{}, err
	}
	return stream.Revision, stream.Reader.Close()
}

// PinOpened identifies the published bytes of a file another owner has just
// opened, hashing through the held descriptor without moving its offset, so
// the pin names exactly the object that owner reads.
func (e ExecutionRevisions) PinOpened(ctx context.Context, resolved projectpaths.Resolved, file *os.File) (toolcontract.ExecutionRevision, bool, error) {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return toolcontract.ExecutionRevision{}, false, err
	}
	captured, err := e.scanFile(ctx, resolved, file, info, false)
	if err != nil {
		return toolcontract.ExecutionRevision{}, false, err
	}
	defer captured.Close()
	return captured.revision, true, nil
}

// OpenStream pins published disk bytes before streaming. Credential inputs
// use the bounded capture owner; regular and compressed inputs verify captured
// block hashes before delivering bytes.
func (e ExecutionRevisions) OpenStream(ctx context.Context, tctx tools.ToolContext, path string, maxBytes int64) (ExecutionStream, error) {
	resolved, err := projectpaths.ResolveRead(ctx, e.Boundary, tctx, path)
	if err != nil {
		return ExecutionStream{}, err
	}
	return e.OpenWalkedStream(ctx, tctx, resolved, nil, maxBytes)
}

// OpenWalkedStream pins published disk bytes for an already-resolved path,
// reusing root when non-nil to avoid re-opening the root directory per file.
func (e ExecutionRevisions) OpenWalkedStream(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, root *fseffect.ReadRoot, maxBytes int64) (ExecutionStream, error) {
	if err := tools.ObserveCommandFileRead(ctx, resolved.Root.ID, resolved.ScopeRel); err != nil {
		return ExecutionStream{}, err
	}
	classified := resolved.Abs
	if _, rel, ok := tctx.SourceLocation(resolved.Abs); ok {
		classified = rel
	}
	if tctx.Effects.CredentialFiles != nil && protectedpath.IsCredentialFile(classified) {
		return e.captureStream(ctx, tctx, resolved, maxBytes)
	}
	if resolved.Compressed {
		return e.openCompressedStream(ctx, tctx, resolved)
	}
	var (
		file *os.File
		err  error
	)
	if root != nil {
		file, err = root.Open(resolved.EffectLocation().Rel)
	} else {
		file, err = fseffect.OpenRead(resolved.EffectLocation())
	}
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
	captured, err := e.scanFile(ctx, resolved, file, info, true)
	if err != nil {
		_ = file.Close()
		return ExecutionStream{}, err
	}
	return ExecutionStream{
		Path:            resolved.DisplayPath,
		Reader:          &verifiedSeekReader{verifiedSource: captured, file: file},
		Revision:        captured.revision,
		FirstNullOffset: captured.firstNull,
		ContentLearned:  captured.learned,
	}, nil
}

func (e ExecutionRevisions) captureStream(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, maxBytes int64) (ExecutionStream, error) {
	if maxBytes <= 0 {
		maxBytes = sourceview.AccessCommand.MaxBytes()
	}
	content, revision, release, err := e.captureBytes(ctx, tctx, resolved, maxBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return ExecutionStream{}, sourceview.PathNotFound("read", resolved.DisplayPath, resolved.Abs)
	}
	if err != nil {
		return ExecutionStream{}, err
	}
	if err := tctx.ObserveCredentialRead(ctx, resolved.Abs, resolved.DisplayPath, string(content)); err != nil {
		release()
		return ExecutionStream{}, fmt.Errorf("record credential file read: %w", err)
	}
	return ExecutionStream{
		Path:            resolved.DisplayPath,
		Reader:          &captureReadCloser{reader: bytes.NewReader(content), release: release},
		Revision:        revision,
		FirstNullOffset: bytes.IndexByte(content, 0),
	}, nil
}

func (b *ExecutionBytes) Close() {
	if b.release != nil {
		b.release()
		b.release = nil
	}
	b.Bytes = nil
}

type captureReadCloser struct {
	reader  io.Reader
	release func()
}

func (r *captureReadCloser) Read(p []byte) (int, error) {
	if r.reader == nil {
		return 0, io.ErrClosedPipe
	}
	return r.reader.Read(p)
}

func (r *captureReadCloser) Close() error {
	r.reader = nil
	if r.release != nil {
		r.release()
		r.release = nil
	}
	return nil
}
