package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/scratch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// OpenOutputView serves the scoped retained body. The pathname is only a view;
// repairing it never establishes source evidence or makes its inode authoritative.
func (r *ProgramRuntime) OpenOutputView(ctx context.Context, tc tools.ToolContext, path projectpaths.Resolved) (io.ReadCloser, error) {
	parent := fspath.CanonicalPath(filepath.Dir(path.Abs))
	relative, pathErr := filepath.Rel(fspath.CanonicalPath(tc.Host.SessionOutputDir), filepath.Join(parent, filepath.Base(path.Abs)))
	output, ok := scratch.ParseCommandOutput(filepath.ToSlash(relative))
	if pathErr != nil || !ok || path.Root.ID != projectroot.VirtualOutputLabel {
		return nil, fmt.Errorf("output view requires the session output path")
	}
	outputs, err := r.sessionOutputs(ctx, tc.Identity.SessionID)
	if err != nil {
		return nil, err
	}
	memory := r.sessionResources(tc.Identity.SessionID)
	reference := "output:program:" + output.ProgramID + ":" + output.Stream
	if output.ExecutionID != "" {
		reference = "output:" + output.ExecutionID + ":" + output.Stream
	}
	body, err := outputs.Open(ctx, tc.Identity.SessionID, output.ProgramID, reference)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep && body != nil {
			_ = body.Reader.Close()
		}
	}()
	release, err := memory.Reserve(ctx, (64<<10)+4096)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !keep {
			release()
		}
	}()
	buffer := make([]byte, 64<<10)
	loc := fseffect.Location{Root: tc.Host.SessionOutputDir, Rel: relative}
	if !outputViewMatches(ctx, loc, body.Blob, buffer) {
		verified := &outputViewBody{ReadCloser: body.Reader, ctx: ctx, blob: body.Blob, digest: sha256.New()}
		err = scratch.PublishCommandOutput(tc.Host.SessionOutputDir, output, verified)
		err = errors.Join(err, verified.Close())
		if err != nil {
			return nil, err
		}
		body, err = outputs.Open(ctx, tc.Identity.SessionID, output.ProgramID, reference)
		if err != nil {
			return nil, err
		}
	}
	keep = true
	return &outputViewBody{ReadCloser: body.Reader, ctx: ctx, blob: body.Blob, digest: sha256.New(), release: release}, nil
}

func outputViewMatches(ctx context.Context, loc fseffect.Location, blob blobstore.Blob, buffer []byte) bool {
	file, err := fseffect.OpenRead(loc)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != blob.Size {
		return false
	}
	digest := sha256.New()
	var count int64
	for {
		if ctx.Err() != nil {
			return false
		}
		n, err := file.Read(buffer)
		count += int64(n)
		if count > blob.Size {
			return false
		}
		_, _ = digest.Write(buffer[:n])
		if err != nil {
			return errors.Is(err, io.EOF) && count == blob.Size && blob.MatchesContentDigest(hex.EncodeToString(digest.Sum(nil)))
		}
		if n == 0 {
			return false
		}
	}
}

// outputViewBody checks retained bytes before a host read can return them.
type outputViewBody struct {
	io.ReadCloser
	ctx      context.Context
	blob     blobstore.Blob
	digest   hash.Hash
	count    int64
	release  func()
	once     sync.Once
	closeErr error
}

func (r *outputViewBody) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.ReadCloser.Read(p)
	r.count += int64(n)
	_, _ = r.digest.Write(p[:n])
	if r.count > r.blob.Size || (errors.Is(err, io.EOF) && (r.count != r.blob.Size || !r.blob.MatchesContentDigest(hex.EncodeToString(r.digest.Sum(nil))))) {
		return n, fmt.Errorf("retained output content identity mismatch")
	}
	return n, err
}

func (r *outputViewBody) Close() error {
	r.once.Do(func() {
		r.closeErr = r.ReadCloser.Close()
		if r.release != nil {
			r.release()
		}
	})
	return r.closeErr
}
