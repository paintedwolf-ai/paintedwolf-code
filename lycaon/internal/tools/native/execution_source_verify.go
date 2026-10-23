package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// recaptureSource validates and hashes the source representation through the
// existing held stream and text decoder. UTF-16 without a BOM keeps the complete
// classifier because a prefix cannot establish its encoding reliably.
func (e ExecutionRevisions) recaptureSource(ctx context.Context, tc tools.ToolContext, resolved projectpaths.Resolved) (revision toolcontract.ExecutionRevision, finalErr error) {
	stream, err := e.OpenStream(ctx, tc, resolved.Abs, 0)
	if err != nil {
		return toolcontract.ExecutionRevision{}, err
	}
	defer func() {
		if closeErr := stream.Reader.Close(); closeErr != nil {
			if finalErr == nil {
				finalErr = closeErr
			} else {
				finalErr = errors.Join(finalErr, closeErr)
			}
		}
	}()
	fallback := func() (toolcontract.ExecutionRevision, error) {
		if err := stream.Reader.Close(); err != nil {
			return toolcontract.ExecutionRevision{}, err
		}
		source, err := e.CaptureSource(ctx, tc, resolved.Abs)
		revision := source.Revision
		source.Close()
		return revision, err
	}
	if stream.Revision.Mode != toolcontract.PublishedDiskRevision {
		return fallback()
	}
	release, err := e.reserveWorking(ctx, 128<<10)
	if err != nil {
		return toolcontract.ExecutionRevision{}, err
	}
	defer release()
	rawHash, contentHash := sha256.New(), sha256.New()
	decoder, err := textfile.NewUTF8Reader(io.TeeReader(stream.Reader, rawHash), "")
	if err != nil {
		return fallback()
	}
	var scratch [32 << 10]byte
	size, err := io.CopyBuffer(contentHash, textStreamReader{ctx: ctx, reader: decoder, path: stream.Path}, scratch[:])
	if err != nil {
		if ctx.Err() != nil {
			return toolcontract.ExecutionRevision{}, ctx.Err()
		}
		return fallback()
	}

	current := stream.Revision
	current.Representation = toolcontract.UTF8SourceBytes
	current.DiskSHA256 = hex.EncodeToString(rawHash.Sum(nil))
	current.ContentSHA256, current.ContentBytes = hex.EncodeToString(contentHash.Sum(nil)), size
	return current, current.Validate()
}
