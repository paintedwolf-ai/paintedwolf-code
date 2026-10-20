package captureprojection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// StreamOptions are complete-stream limits and an optional admitted span sink.
// Observe must reserve any metadata it retains; no span list is retained here.
type StreamOptions struct {
	WorkingMemory secretmatch.StreamMemoryAdmission
	Bounds        secretmatch.StreamBounds
	Observe       func(context.Context, secretmatch.StreamSpan) error
}

// StreamMetadata describes actual transferred bytes, including a terminal prefix.
// Complete is true only after EOF and the final generation/revision checks pass.
type StreamMetadata struct {
	RawBytes, EmittedBytes                 int64
	RawSHA256, EmittedSHA256               string
	Redacted, Complete                     bool
	CatalogVersion, ClassificationRevision string
	ManagedGeneration                      uint64
}

// ScreenStream emits only screened bytes. The source must honor its own context
// or close protocol; an arbitrary blocked io.Reader cannot be canceled by Go.
// A terminal error invalidates complete-artifact publication, even if a screened
// prefix was emitted. Publication rechecks the returned generation and revision.
func (p *Projector) ScreenStream(ctx context.Context, scope Scope, label string, src io.Reader, dst io.Writer, options StreamOptions) (metadata StreamMetadata, err error) {
	if p == nil || p.matcher == nil || options.WorkingMemory == nil || src == nil || dst == nil {
		return metadata, ErrUnavailable
	}
	ctx = scopeContext(ctx, scope)
	if err := options.WorkingMemory.ReserveBytes(ctx, 1024); err != nil {
		return metadata, err
	}
	defer options.WorkingMemory.ReleaseBytes(1024)
	if err := p.prepare(ctx, scope); err != nil {
		return metadata, err
	}
	if p.generation != nil {
		metadata.ManagedGeneration = p.generation()
	}
	metadata.CatalogVersion = p.matcher.CatalogVersion()
	raw, emitted := sha256.New(), sha256.New()
	check := func() error {
		if p.generation != nil && p.generation() != metadata.ManagedGeneration {
			return &secretmatch.StreamChangedError{Resource: "managed_generation"}
		}
		return nil
	}
	result, err := p.matcher.ScreenBytes(ctx, io.TeeReader(src, raw), streamHashWriter{dst, emitted}, label, options.Bounds, options.WorkingMemory, check, options.Observe)
	metadata.RawBytes = result.InputBytes
	metadata.EmittedBytes = result.OutputBytes
	metadata.Redacted = result.Redacted
	metadata.ClassificationRevision = result.ClassificationRevision
	metadata.RawSHA256 = hex.EncodeToString(raw.Sum(nil))
	metadata.EmittedSHA256 = hex.EncodeToString(emitted.Sum(nil))
	metadata.Complete = err == nil
	return metadata, err
}

type streamHashWriter struct {
	writer io.Writer
	hash   hash.Hash
}

func (w streamHashWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	if n >= 0 && n <= len(p) {
		_, _ = w.hash.Write(p[:n])
	}
	return n, err
}
