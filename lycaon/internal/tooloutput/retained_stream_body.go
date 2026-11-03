package tooloutput

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/zstdcodec"
)

// RetainedStreamReference is the exact body identity a durable output receipt
// persists. Initiating provenance never substitutes for the actual retention claim.
type RetainedStreamReference struct {
	Blob              blobstore.Blob
	RetentionClaimID  string
	InitiatingClaimID string
}

// RetainedStreamBody owns one actual retention claim, disk reservation and body
// metadata lease. Its existing invocation/output lifecycle must keep it until
// Close physically collects the body; boot reconstruction belongs to that owner.
type RetainedStreamBody struct {
	reference       RetainedStreamReference
	store           blobstore.Store
	release         func()
	memory          StreamMemoryAdmission
	releaseState    func()
	mu              sync.Mutex
	closed, closing bool
	active          int
}

func (s *RetainedStreamBody) Reference() RetainedStreamReference { return s.reference }

func (s *RetainedStreamBody) Open(ctx context.Context) (io.ReadCloser, error) {
	s.mu.Lock()
	closed := s.closed || s.closing
	s.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("stream spill has been released")
	}
	// Admission may wait for another reader's lease. Never retain the spill
	// mutex while waiting: that reader needs it to finish closing.
	release, err := s.memory.Reserve(ctx, zstdcodec.StreamReaderMemoryReservation)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("stream reader memory admission returned no release")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	if s.closed || s.closing {
		release()
		return nil, fmt.Errorf("stream spill has been released")
	}
	reader, err := s.store.OpenStream(s.reference.Blob)
	if err != nil {
		release()
		return nil, err
	}
	s.active++
	return &spillReader{ReadCloser: reader, spill: s, release: release}, nil
}

// Close relinquishes this claim and asks the blob store to discard an unclaimed
// body. Accounting stays reserved if another recovery claim prevents collection;
// retry after that claim is released. Dropping a reference never frees quota
// while the corresponding retained bytes still exist.
func (s *RetainedStreamBody) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closing = true
	if s.active != 0 {
		return fmt.Errorf("stream spill still has active readers; accounting remains reserved")
	}
	// A durable attachment requires this EXACT claim during recovery. Refusing
	// collection after deleting its marker would strand that attachment on boot.
	if s.store.Retained != nil {
		retained, err := s.store.Retained(s.reference.Blob.ID)
		if err != nil {
			return err
		}
		if retained {
			return fmt.Errorf("stream body has a durable reference; claim and accounting remain reserved")
		}
	}
	if err := s.store.Release(s.reference.RetentionClaimID, []string{s.reference.Blob.ID}); err != nil {
		return err
	}
	removed, err := s.store.DiscardStagedBefore(s.reference.Blob.ID, time.Now().Add(time.Second))
	if err != nil {
		return err
	}
	if !removed {
		return fmt.Errorf("stream spill still retained; accounting remains reserved")
	}
	s.closed = true
	s.release()
	s.releaseState()
	return nil
}

type spillReader struct {
	io.ReadCloser
	spill   *RetainedStreamBody
	once    sync.Once
	err     error
	release func()
}

func (r *spillReader) Close() error {
	r.once.Do(func() {
		r.err = r.ReadCloser.Close()
		r.release()
		r.spill.mu.Lock()
		r.spill.active--
		r.spill.mu.Unlock()
	})
	return r.err
}
