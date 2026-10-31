package native

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"

	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

const sourceBlockBytes = 32 << 10

var sourceBlockBufferPool = sync.Pool{
	New: func() any {
		buf := new([sourceBlockBytes]byte)
		return buf
	},
}

// verifiedSource owns the capture's ordered block proofs and its one reusable
// block buffer. The full pin hashes those same ordered bytes. A disk capture is
// not an atomic filesystem snapshot; later reads must prove every emitted block.
type verifiedSource struct {
	ctx            context.Context
	revision       toolcontract.ExecutionRevision
	path           string
	firstNull      int
	blocks         [][sha256.Size]byte
	buffer         []byte
	pooled         *[sourceBlockBytes]byte
	working, state func()
	position       int64
	cached         int64
	valid          int
	closed         bool
	learned        bool
}

func (e ExecutionRevisions) scanSource(ctx context.Context, resolved projectpaths.Resolved, input io.Reader, proofs bool) (_ *verifiedSource, err error) {
	release, err := e.reserveWorking(ctx, sourceBlockBytes)
	if err != nil {
		return nil, err
	}
	pooled := sourceBlockBufferPool.Get().(*[sourceBlockBytes]byte)
	capture := &verifiedSource{ctx: ctx, path: resolved.DisplayPath, firstNull: -1, buffer: pooled[:], pooled: pooled, working: release, cached: -1}
	defer func() {
		if err != nil {
			capture.Close()
		}
	}()
	hash := sha256.New()
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, readErr := io.ReadFull(input, capture.buffer)
		if n > 0 {
			// Reserve the next proof slot before growing its backing array.
			if proofs && len(capture.blocks) == cap(capture.blocks) {
				capacity := max(8, 2*cap(capture.blocks))
				acquired, admissionErr := e.reserveCapture(ctx, int64(capacity)*sha256.Size)
				if admissionErr != nil {
					return nil, admissionErr
				}
				next := make([][sha256.Size]byte, len(capture.blocks), capacity)
				copy(next, capture.blocks)
				capture.blocks = next
				if capture.state != nil {
					capture.state()
				}
				capture.state = acquired
			}
			chunk := capture.buffer[:n]
			_, _ = hash.Write(chunk)
			if proofs {
				capture.blocks = append(capture.blocks, sha256.Sum256(chunk))
			}
			if capture.firstNull < 0 {
				if index := bytes.IndexByte(chunk, 0); index >= 0 {
					capture.firstNull = int(total) + index
				}
			}
			total += int64(n)
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	root, rel := revisionLocation(resolved)
	digest := hex.EncodeToString(hash.Sum(nil))
	capture.revision = toolcontract.ExecutionRevision{Role: toolcontract.SourceRevision, Mode: toolcontract.PublishedDiskRevision, Basis: toolcontract.CapturedRevision, RootID: root, Path: rel, DiskSHA256: digest, Representation: toolcontract.OriginalEncodedBytes, ContentSHA256: digest, ContentBytes: total}
	if err := capture.revision.Validate(); err != nil {
		return nil, err
	}
	return capture, nil
}

func (r *verifiedSource) conflict(cause error) error {
	return &ExecutionRevisionConflict{Path: r.path, Pinned: r.revision, Cause: cause}
}

// Metadata detects ordinary concurrent capture changes; block proofs, rather
// than metadata, establish the identity of bytes subsequently delivered.
func (r *verifiedSource) checkFile(file *os.File, before fs.FileInfo) error {
	after, err := file.Stat()
	if err != nil {
		return r.conflict(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return r.conflict(nil)
	}
	return nil
}

func (r *verifiedSource) read(p []byte, load func(int64, []byte) (int, error)) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.position >= r.revision.ContentBytes {
		return 0, io.EOF
	}
	block := r.position / sourceBlockBytes
	if r.cached != block {
		size := int(min(int64(sourceBlockBytes), r.revision.ContentBytes-block*sourceBlockBytes))
		n, err := load(block*sourceBlockBytes, r.buffer[:size])
		if n != size || (err != nil && !errors.Is(err, io.EOF)) {
			return 0, r.conflict(err)
		}
		if sha256.Sum256(r.buffer[:size]) != r.blocks[block] {
			return 0, r.conflict(nil)
		}
		r.cached, r.valid = block, size
	}
	offset := int(r.position % sourceBlockBytes)
	n := copy(p, r.buffer[offset:r.valid])
	r.position += int64(n)
	return n, nil
}

func (r *verifiedSource) Close() {
	if !r.closed {
		r.closed = true
		r.blocks, r.buffer = nil, nil
		if r.pooled != nil {
			sourceBlockBufferPool.Put(r.pooled)
			r.pooled = nil
		}
		if r.state != nil {
			r.state()
			r.state = nil
		}
		if r.working != nil {
			r.working()
			r.working = nil
		}
	}
}

type verifiedSeekReader struct {
	*verifiedSource
	file *os.File
}

func (r *verifiedSeekReader) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	info, err := r.file.Stat()
	if err != nil {
		return 0, r.conflict(err)
	}
	if info.Size() != r.revision.ContentBytes {
		return 0, r.conflict(nil)
	}
	return r.read(p, func(offset int64, block []byte) (int, error) { return r.file.ReadAt(block, offset) })
}

func (r *verifiedSeekReader) Seek(offset int64, whence int) (int64, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.position
	case io.SeekEnd:
		offset += r.revision.ContentBytes
	default:
		return r.position, fs.ErrInvalid
	}
	if offset < 0 {
		return r.position, fs.ErrInvalid
	}
	r.position = offset
	return offset, nil
}

func (r *verifiedSeekReader) Close() error {
	if r.closed {
		return nil
	}
	r.verifiedSource.Close()
	return r.file.Close()
}
