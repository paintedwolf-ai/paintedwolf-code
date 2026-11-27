package portablepython

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/pythonevidence"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

const defaultRetainedBlobLimit = 16 * 1024 * 1024 // 16 MiB per retained stream blob

// BlobRetainedStreamPort implements pythonevidence.RetainedStreamPort on top
// of Painted Wolf's disk-backed blobstore.Store, persisting screened overflow
// stdout/stderr streams as retained content-addressed blobs.
type BlobRetainedStreamPort struct {
	store blobstore.Store
	limit bytebound.Materialization
}

var _ pythonevidence.RetainedStreamPort = (*BlobRetainedStreamPort)(nil)

// NewBlobRetainedStreamPort constructs a disk-backed RetainedStreamPort using
// store for durable retained-output persistence.
func NewBlobRetainedStreamPort(store blobstore.Store) *BlobRetainedStreamPort {
	return &BlobRetainedStreamPort{
		store: store,
		limit: bytebound.Materialization(defaultRetainedBlobLimit),
	}
}

func (p *BlobRetainedStreamPort) StoreScreened(_ context.Context, artifactID string, content tooloutput.ScreenedOutput) (tooloutput.RetainedStreamReference, error) {
	name := strings.TrimSpace(artifactID)
	if name == "" {
		name = "python-output.txt"
	} else if !strings.HasSuffix(name, ".txt") {
		name += ".txt"
	}
	blob, err := p.store.Put(name, strings.NewReader(content.String()), p.limit)
	if err != nil {
		return tooloutput.RetainedStreamReference{}, fmt.Errorf("store retained python stream: %w", err)
	}
	claimID := "py-ret-" + artifactID
	if _, err := p.store.Retain(claimID, []string{blob.ID}); err != nil {
		return tooloutput.RetainedStreamReference{}, fmt.Errorf("retain python stream blob: %w", err)
	}
	return tooloutput.RetainedStreamReference{
		Blob:              blob,
		RetentionClaimID:  claimID,
		InitiatingClaimID: claimID,
	}, nil
}

func (p *BlobRetainedStreamPort) LoadScreened(_ context.Context, ref tooloutput.RetainedStreamReference) ([]byte, error) {
	rc, err := p.store.Open(ref.Blob)
	if err != nil {
		return nil, fmt.Errorf("open retained python stream blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	return io.ReadAll(rc)
}

func (p *BlobRetainedStreamPort) ReleaseStream(_ context.Context, ref tooloutput.RetainedStreamReference) error {
	if ref.RetentionClaimID == "" || ref.Blob.ID == "" {
		return nil
	}
	return p.store.Release(ref.RetentionClaimID, []string{ref.Blob.ID})
}
