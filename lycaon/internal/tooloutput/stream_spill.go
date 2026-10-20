package tooloutput

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
)

// ScreenedStream admits a reader whose bytes have crossed durable screening.
// Screening is performed by the capture owner before any byte enters this port.
type ScreenedStream struct{ reader io.ReadCloser }

func ScreenedReader(reader io.ReadCloser) ScreenedStream { return ScreenedStream{reader: reader} }

// SpillAdmission reserves staging and retained output against invocation/session
// quotas before storage starts. The caller retains the lease while this output
// is reachable; it must separately account the codec's working memory.
type SpillAdmission interface {
	ReserveSpill(context.Context, int64) (release func(), err error)
}

type StreamSpillOptions struct {
	Store        blobstore.Store
	ClaimID      string
	MaximumBytes int64
	PreviewBytes int
	Admission    SpillAdmission
}

// StreamSpill separates retained bytes, a display prefix and stream settlement.
// A readable failure prefix is incomplete and never publication eligibility.
// No new durable receipt shape is written here; callers record references using
// their existing persistence/recovery contract when introduced.
type StreamSpill struct {
	Blob          blobstore.Blob
	Preview       []byte
	EmittedSHA256 string
	Complete      bool
	Failure       error
	store         blobstore.Store
	claim         string
	release       func()
	mu            sync.Mutex
	closed        bool
	active        int
}

func RetainScreenedStream(ctx context.Context, input ScreenedStream, options StreamSpillOptions) (*StreamSpill, error) {
	if ctx == nil || input.reader == nil || options.Admission == nil || strings.TrimSpace(options.ClaimID) == "" || options.MaximumBytes <= 0 || options.MaximumBytes > 256<<20 || options.PreviewBytes < 0 || options.PreviewBytes > 16<<10 {
		return nil, fmt.Errorf("screened stream spill requires explicit bounded input, claim and quota admission")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Plaintext and compressed stages overlap, followed by two compressed copies
	// during replacement. Three plaintext ceilings plus a frame allowance cover
	// that bounded-window encoder's worst-case storage, including tiny inputs.
	release, err := options.Admission.ReserveSpill(ctx, 3*options.MaximumBytes+(1<<20))
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("spill admission returned no release")
	}
	defer input.reader.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = input.reader.Close()
		case <-done:
		}
	}()
	store := options.Store
	store.Dir = ToolOutputSpillDir
	store.StagedTTL = 7 * 24 * time.Hour
	reader := &spillInput{ctx: ctx, source: input.reader, maximum: options.MaximumBytes, previewMaximum: options.PreviewBytes, digest: sha256.New()}
	blob, err := store.Put("command-stream.bin", reader, bytebound.Materialization(options.MaximumBytes))
	if err != nil {
		release()
		return nil, err
	}
	if ctx.Err() != nil {
		reader.failure = ctx.Err()
		reader.eof = false
	}
	if _, err := store.Retain(options.ClaimID, []string{blob.ID}); err != nil {
		release()
		return nil, err
	}
	return &StreamSpill{Blob: blob, Preview: reader.preview, EmittedSHA256: hex.EncodeToString(reader.digest.Sum(nil)), Complete: reader.failure == nil && reader.eof, Failure: reader.failure, store: store, claim: options.ClaimID, release: release}, nil
}

func (s *StreamSpill) Open() (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("stream spill has been released")
	}
	reader, err := s.store.Open(s.Blob)
	if err != nil {
		return nil, err
	}
	s.active++
	return &spillReader{ReadCloser: reader, spill: s}, nil
}

// Close relinquishes this claim and asks the blob store to discard an unclaimed
// body. Accounting stays reserved if another recovery claim prevents collection;
// retry after that claim is released. Dropping a reference never frees quota
// while the corresponding retained bytes still exist.
func (s *StreamSpill) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if s.active != 0 {
		return fmt.Errorf("stream spill still has active readers; accounting remains reserved")
	}
	if err := s.store.Release(s.claim, []string{s.Blob.ID}); err != nil {
		return err
	}
	removed, err := s.store.DiscardStagedBefore(s.Blob.ID, time.Now().Add(time.Second))
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("stream spill still retained; accounting remains reserved")
	}
	s.closed = true
	s.release()
	return nil
}

type spillInput struct {
	ctx               context.Context
	source            io.Reader
	maximum, received int64
	previewMaximum    int
	preview           []byte
	digest            hash.Hash
	failure           error
	eof               bool
}

func (r *spillInput) Read(b []byte) (int, error) {
	if r.failure != nil || r.eof {
		return 0, io.EOF
	}
	if err := r.ctx.Err(); err != nil {
		r.failure = err
		return 0, io.EOF
	}
	n, err := r.source.Read(b)
	if canceled := r.ctx.Err(); canceled != nil {
		r.failure = canceled
		return 0, io.EOF
	}
	if int64(n) > r.maximum-r.received {
		r.failure = fmt.Errorf("screened stream spill exceeds %d bytes", r.maximum)
		return 0, io.EOF
	}
	r.received += int64(n)
	_, _ = r.digest.Write(b[:n])
	if remaining := r.previewMaximum - len(r.preview); remaining > 0 {
		r.preview = append(r.preview, b[:min(n, remaining)]...)
	}
	if errors.Is(err, io.EOF) {
		r.eof = true
	} else if err != nil {
		r.failure = err
	}
	// Retain an already-screened failure prefix with explicit incomplete metadata;
	// returning EOF to storage never converts that prefix to a complete result.
	return n, nil
}

type spillReader struct {
	io.ReadCloser
	spill *StreamSpill
	once  sync.Once
	err   error
}

func (r *spillReader) Close() error {
	r.once.Do(func() {
		r.err = r.ReadCloser.Close()
		r.spill.mu.Lock()
		r.spill.active--
		r.spill.mu.Unlock()
	})
	return r.err
}
