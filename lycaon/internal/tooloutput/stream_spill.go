package tooloutput

import (
	"bytes"
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

// StreamSpill separates retained bytes, display text and stream settlement.
// A readable failure prefix is incomplete and never publication eligibility.
// Callers record references through their own persistence and recovery contract.
type StreamSpill struct {
	Blob           blobstore.Blob
	Display        StreamDisplay
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

// StreamDisplay is the screened text kept beside a retained body for display.
type StreamDisplay struct {
	Preview []byte
	// Tail holds the stream's last bytes whenever a preview was requested.
	Tail []byte
	// Lines counts newline-terminated lines plus an unterminated final line.
	Lines int64
}

// RetainedFrameBytes covers a retained blob's framing and filesystem slack.
const RetainedFrameBytes = 4096

// StreamTailBytes bounds the tail kept beside a preview.
const StreamTailBytes = 1024

func RetainScreenedStream(ctx context.Context, input ScreenedStream, options StreamSpillOptions) (*StreamSpill, error) {
	if ctx == nil || input.reader == nil || options.Admission == nil || options.WorkingMemory == nil || options.RetainedState == nil || strings.TrimSpace(options.ClaimID) == "" || options.MaximumBytes <= 0 || options.MaximumBytes > 256<<20 || options.PreviewBytes < 0 || options.PreviewBytes > 16<<10 {
		return nil, fmt.Errorf("screened stream spill requires explicit bounded input, claim and quota admission")
	}
	defer func() { _ = input.reader.Close() }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	staging, err := newStreamSpillStaging(ctx, options.Admission, options.MaximumBytes)
	if err != nil {
		return nil, err
	}
	defer staging.Close()
	releaseState, err := options.RetainedState.ReserveProgramState(ctx, int64(len(options.ClaimID))*2+512)
	if err != nil {
		return nil, err
	}
	if releaseState == nil {
		return nil, fmt.Errorf("stream state admission returned no release")
	}
	tailBytes := 0
	if options.PreviewBytes > 0 {
		tailBytes = StreamTailBytes
	}
	releasePreview, err := options.RetainedState.ReserveProgramState(ctx, int64(options.PreviewBytes+tailBytes)+256)
	if err != nil || releasePreview == nil {
		releaseState()
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
	reader := &spillInput{ctx: ctx, source: input.reader, maximum: options.MaximumBytes, display: newDisplayCapture(options.PreviewBytes, tailBytes), digest: sha256.New(), staging: staging}
	blob, err := store.PutStream(ctx, "command-stream.bin", reader, bytebound.Materialization(options.MaximumBytes), options.WorkingMemory)
	staging.Close()
	if err != nil {
		return nil, err
	}
	// The maximum preview is scratch while reading. Once the stream settles,
	// retain only bytes that were actually captured.
	display := reader.display.settle()
	releasePreview()
	settledPreview, reserveErr := options.RetainedState.ReserveProgramState(ctx, int64(len(display.Preview)+len(display.Tail))+256)
	if reserveErr != nil || settledPreview == nil {
		err = reserveErr
		if err == nil {
			err = fmt.Errorf("preview state admission returned no release")
		}
		return nil, err
	}
	releasePreview = settledPreview
	// Retention holds the stored bytes, not the encoder's worst case.
	release, err := options.Admission.ReserveSpill(ctx, blob.Size+RetainedFrameBytes)
	if err != nil || release == nil {
		if err == nil {
			err = fmt.Errorf("spill admission returned no release")
		}
		_, discardErr := store.DiscardStagedBefore(blob.ID, time.Now().Add(time.Second))
		return nil, errors.Join(err, discardErr)
	}
	if ctx.Err() != nil {
		reader.failure = ctx.Err()
		reader.eof = false
	}
	claim := options.ClaimID + ":stream-reference:" + uuid.NewString()
	reference := RetainedStreamReference{Blob: blob, RetentionClaimID: claim, InitiatingClaimID: strings.Clone(options.ClaimID)}
	body := &RetainedStreamBody{reference: reference, store: store, release: release, memory: options.WorkingMemory, releaseState: releaseState}
	out := &StreamSpill{Blob: blob, Display: display, EmittedSHA256: hex.EncodeToString(reader.digest.Sum(nil)), Complete: reader.failure == nil && reader.eof, Failure: reader.failure, body: body, reference: reference, releasePreview: releasePreview}
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
	display           displayCapture
	digest            hash.Hash
	failure           error
	eof               bool
	staging           *streamSpillStaging
}

func (r *spillInput) Read(b []byte) (int, error) {
	if r.failure != nil || r.eof {
		return 0, io.EOF
	}
	if err := r.ctx.Err(); err != nil {
		r.failure = err
		return 0, io.EOF
	}
	b = b[:min(int64(len(b)), 64<<10, r.maximum-r.received+1)]
	if err := r.staging.Grow(min(r.maximum, r.received+int64(len(b)))); err != nil {
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
	r.display.observe(b[:n])
	if errors.Is(err, io.EOF) {
		r.eof = true
	} else if err != nil {
		r.failure = err
	}
	// Retain an already-screened failure prefix with explicit incomplete metadata;
	// returning EOF to storage never converts that prefix to a complete result.
	return n, nil
}

// displayCapture observes a stream once for its preview, tail and line count.
type displayCapture struct {
	previewMaximum int
	preview        []byte
	tail           tailRing
	newlines       int64
	last           byte
	seen           bool
}

func newDisplayCapture(preview, tail int) displayCapture {
	return displayCapture{previewMaximum: preview, preview: make([]byte, 0, preview), tail: tailRing{buf: make([]byte, tail)}}
}

func (d *displayCapture) observe(b []byte) {
	if len(b) == 0 {
		return
	}
	if remaining := d.previewMaximum - len(d.preview); remaining > 0 {
		d.preview = append(d.preview, b[:min(len(b), remaining)]...)
	}
	d.tail.write(b)
	d.newlines += int64(bytes.Count(b, []byte{'\n'}))
	d.last, d.seen = b[len(b)-1], true
}

func (d *displayCapture) settle() StreamDisplay {
	lines := d.newlines
	if d.seen && d.last != '\n' {
		lines++
	}
	return StreamDisplay{Preview: d.preview, Tail: d.tail.bytes(), Lines: lines}
}

// tailRing keeps the last len(buf) bytes written.
type tailRing struct {
	buf    []byte
	next   int
	filled bool
}

func (t *tailRing) write(b []byte) {
	if len(t.buf) == 0 {
		return
	}
	if len(b) >= len(t.buf) {
		copy(t.buf, b[len(b)-len(t.buf):])
		t.next, t.filled = 0, true
		return
	}
	n := copy(t.buf[t.next:], b)
	if n < len(b) {
		copy(t.buf, b[n:])
		t.filled = true
	}
	t.next = (t.next + len(b)) % len(t.buf)
	if t.next == 0 {
		t.filled = true
	}
}

func (t *tailRing) bytes() []byte {
	if !t.filled {
		return append([]byte(nil), t.buf[:t.next]...)
	}
	return append(append([]byte(nil), t.buf[t.next:]...), t.buf[:t.next]...)
}
