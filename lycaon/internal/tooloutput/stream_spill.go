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

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
)

// ScreenedStream admits a reader whose bytes have crossed durable screening.
// Screening is performed by the capture owner before any byte enters this port.
type ScreenedStream struct{ reader io.ReadCloser }

func ScreenedReader(reader io.ReadCloser) ScreenedStream { return ScreenedStream{reader: reader} }

// SpillAdmission reserves staging and retained output against invocation/session
// quotas before storage starts. The caller retains the lease while this output
// is reachable; WorkingMemory supplies the codec reservation independently.
type SpillAdmission interface {
	ReserveSpill(context.Context, int64) (release func(), err error)
}

type StreamMemoryAdmission interface {
	Reserve(context.Context, int64) (release func(), err error)
}

// StreamStateAdmission keeps retained preview and metadata charged separately
// from transient screening/codec scratch until the spill reference is released.
type StreamStateAdmission interface {
	ReserveProgramState(context.Context, int64) (release func(), err error)
}

type StreamSpillOptions struct {
	Store         blobstore.Store
	ClaimID       string
	MaximumBytes  int64
	PreviewBytes  int
	Admission     SpillAdmission
	WorkingMemory StreamMemoryAdmission
	RetainedState StreamStateAdmission
}

// StreamSpillLimitError reports an attempted byte admission before disk writes.
type StreamSpillLimitError struct{ Maximum, Observed int64 }

func (e *StreamSpillLimitError) Error() string {
	return fmt.Sprintf("screened stream spill byte ceiling exceeded: %d > %d", e.Observed, e.Maximum)
}

// StreamSpill separates retained bytes, a display prefix and stream settlement.
// A readable failure prefix is incomplete and never publication eligibility.
// No new durable receipt shape is written here; callers record references using
// their existing persistence/recovery contract when introduced.
type StreamSpill struct {
	Blob           blobstore.Blob
	Preview        []byte
	EmittedSHA256  string
	Complete       bool
	Failure        error
	mu             sync.Mutex
	body           *RetainedStreamBody
	reference      RetainedStreamReference
	releasePreview func()
	opening        int
	closed         bool
}

func RetainScreenedStream(ctx context.Context, input ScreenedStream, options StreamSpillOptions) (*StreamSpill, error) {
	if ctx == nil || input.reader == nil || options.Admission == nil || options.WorkingMemory == nil || options.RetainedState == nil || strings.TrimSpace(options.ClaimID) == "" || options.MaximumBytes <= 0 || options.MaximumBytes > 256<<20 || options.PreviewBytes < 0 || options.PreviewBytes > 16<<10 {
		return nil, fmt.Errorf("screened stream spill requires explicit bounded input, claim and quota admission")
	}
	defer input.reader.Close()
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
	releaseState, err := options.RetainedState.ReserveProgramState(ctx, int64(len(options.ClaimID))*2+4096)
	if err != nil {
		release()
		return nil, err
	}
	if releaseState == nil {
		release()
		return nil, fmt.Errorf("stream state admission returned no release")
	}
	releasePreview, err := options.RetainedState.ReserveProgramState(ctx, int64(options.PreviewBytes)+256)
	if err != nil || releasePreview == nil {
		releaseState()
		release()
		if err == nil {
			err = fmt.Errorf("preview state admission returned no release")
		}
		return nil, err
	}
	retained := false
	defer func() {
		if !retained {
			releaseState()
			releasePreview()
		}
	}()
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
	reader := &spillInput{ctx: ctx, source: input.reader, maximum: options.MaximumBytes, previewMaximum: options.PreviewBytes, preview: make([]byte, 0, options.PreviewBytes), digest: sha256.New()}
	blob, err := store.PutStream(ctx, "command-stream.bin", reader, bytebound.Materialization(options.MaximumBytes), options.WorkingMemory)
	if err != nil {
		release()
		return nil, err
	}
	if ctx.Err() != nil {
		reader.failure = ctx.Err()
		reader.eof = false
	}
	claim := options.ClaimID + ":stream-reference:" + uuid.NewString()
	reference := RetainedStreamReference{Blob: blob, RetentionClaimID: claim, InitiatingClaimID: strings.Clone(options.ClaimID)}
	body := &RetainedStreamBody{reference: reference, store: store, release: release, memory: options.WorkingMemory, releaseState: releaseState}
	out := &StreamSpill{Blob: blob, Preview: reader.preview, EmittedSHA256: hex.EncodeToString(reader.digest.Sum(nil)), Complete: reader.failure == nil && reader.eof, Failure: reader.failure, body: body, reference: reference, releasePreview: releasePreview}
	retained = true
	if _, err := store.Retain(claim, []string{blob.ID}); err != nil {
		out.Complete = false
		out.Failure = err
		return out, errors.Join(err, out.Close())
	}
	return out, nil
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
		r.failure = &StreamSpillLimitError{Maximum: r.maximum, Observed: r.received + int64(n)}
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
