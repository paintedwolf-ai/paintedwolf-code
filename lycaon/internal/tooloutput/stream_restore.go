package tooloutput

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// RestoreRetainedStreamBody rebuilds accounting for an already durable reference.
// The invocation owner reserves disk and body-state leases before calling. On
// refusal those provisional leases are released; boot must not admit new work
// until every durable reference has been reconstructed or reported unavailable.
// It never creates a claim or asserts that the original producer completed.
func RestoreRetainedStreamBody(ctx context.Context, store blobstore.Store, reference RetainedStreamReference, releaseDisk, releaseState func(), memory StreamMemoryAdmission) (body *RetainedStreamBody, err error) {
	defer func() {
		if err != nil {
			if releaseDisk != nil {
				releaseDisk()
			}
			if releaseState != nil {
				releaseState()
			}
		}
	}()
	if ctx == nil || memory == nil || releaseDisk == nil || releaseState == nil || store.Retained == nil {
		return nil, fmt.Errorf("stream recovery requires durable retention and admitted body leases")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	blob := reference.Blob
	if blob.ID == "" || blob.Name != "command-stream.bin" || blob.Size < 0 || blob.Size > 256<<20 || reference.RetentionClaimID == "" || reference.InitiatingClaimID == "" || !strings.HasPrefix(reference.RetentionClaimID, reference.InitiatingClaimID+":stream-reference:") {
		return nil, fmt.Errorf("invalid retained command stream identity")
	}
	store.Dir = ToolOutputSpillDir
	// Persisted local paths are never used to open data; the blob store resolves
	// the exact content identity beneath its own root.
	if blob.Rel != ToolOutputSpillDir+"/"+blob.ID+"/"+blob.Name {
		return nil, fmt.Errorf("retained stream path disagrees with content identity")
	}
	durable, err := store.Retained(blob.ID)
	if err != nil {
		return nil, err
	}
	if !durable {
		return nil, fmt.Errorf("retained command stream has no durable reference")
	}
	claimed, err := store.HasRetentionClaim(reference.RetentionClaimID, blob.ID)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, fmt.Errorf("retained command stream claim is missing")
	}
	releaseReader, err := memory.Reserve(ctx, zstdcodec.StreamReaderMemoryReservation+(32<<10))
	if err != nil {
		return nil, err
	}
	if releaseReader == nil {
		return nil, fmt.Errorf("stream recovery reader admission returned no lease")
	}
	defer releaseReader()
	if err = store.VerifyStream(ctx, blob, make([]byte, 32<<10)); err != nil {
		return nil, err
	}
	return &RetainedStreamBody{reference: reference, store: store, release: releaseDisk, memory: memory, releaseState: releaseState}, nil
}
