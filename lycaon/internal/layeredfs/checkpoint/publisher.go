package checkpoint

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

// FaultStage identifies a deterministic fault-injection boundary in the publication transaction.
type FaultStage string

const (
	FaultNone               FaultStage = ""
	FaultAfterBarrier       FaultStage = "after_barrier"
	FaultOnFlush            FaultStage = "on_flush"
	FaultAfterBlobWrite     FaultStage = "after_blob_write"
	FaultBeforeManifestSync FaultStage = "before_manifest_sync"
	FaultOnRetentionCommit  FaultStage = "on_retention_commit"
)

// MappedRegion represents an active writable memory mapping held by a worker process or adapter.
type MappedRegion struct {
	Path  layeredfs.QualifiedPath
	Flush func() error
}

// BarrierCallbacks hooks adapter/process quiescing, dirty-page flushing, retention, and fault injection.
type BarrierCallbacks struct {
	QuiesceWorkers       func(ctx context.Context) error
	FlushClientDirtyData func(ctx context.Context) error
	ResumeWorkers        func(ctx context.Context, publishErr error)
	CommitRetention      func(ctx context.Context, checkpointID string, blobs []string, gitObjects []string) error
	FaultInjector        func(stage FaultStage) error
}

// PublisherConfig configures a checkpoint Publisher for a worker view.
type PublisherConfig struct {
	Core               *layeredfs.Core
	Store              *Store
	WorkerID           string
	TaskID             string
	ProjectID          string
	SourceGeneration   string
	SelectedRoots      []layeredfs.RootSpec
	ParentCheckpoints  []string
	MaxCheckpointFiles int
	MaxCheckpointBytes int64
	Callbacks          BarrierCallbacks
}

// PublishRequest describes a request to capture and publish a durable checkpoint.
type PublishRequest struct {
	CheckpointID       string
	Trigger            TriggerKind
	CommandID          string
	BatchID            string
	SourceObservations []SourceObservation
	PrivateGit         PrivateGitSnapshot
	CandidateBases     map[layeredfs.QualifiedPath][]layeredfs.BaseCapture
	FaultInjector      func(stage FaultStage) error
}

// PublishResult reports the published manifest and the number of coalesced requests it satisfied.
type PublishResult struct {
	Manifest       Manifest
	CoalescedCount int
}

type pendingPublish struct {
	req  PublishRequest
	done chan publishOutcome
}

type publishOutcome struct {
	res PublishResult
	err error
}

// Publisher executes the 6-step checkpoint publication transaction with request coalescing.
type Publisher struct {
	cfg      PublisherConfig
	seq      atomic.Uint64
	mu       sync.Mutex
	active   bool
	queue    []*pendingPublish
	mappings []*MappedRegion
}

// NewPublisher creates a checkpoint Publisher bound to a layeredfs.Core and Store.
func NewPublisher(cfg PublisherConfig) (*Publisher, error) {
	if cfg.Core == nil || cfg.Store == nil {
		return nil, fmt.Errorf("checkpoint: Core and Store are required")
	}
	if cfg.WorkerID == "" || cfg.ProjectID == "" {
		return nil, fmt.Errorf("checkpoint: WorkerID and ProjectID are required")
	}
	return &Publisher{cfg: cfg}, nil
}

// RegisterMappedRegion registers a writable memory mapping so dirty pages are flushed at Step 2.
func (p *Publisher) RegisterMappedRegion(reg *MappedRegion) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mappings = append(p.mappings, reg)
}

// Publish executes or coalesces a durable 6-step checkpoint publication transaction.
func (p *Publisher) Publish(ctx context.Context, req PublishRequest) (PublishResult, error) {
	if req.Trigger == "" {
		req.Trigger = TriggerExplicitCapture
	}
	item := &pendingPublish{req: req, done: make(chan publishOutcome, 1)}
	p.mu.Lock()
	p.queue = append(p.queue, item)
	if p.active {
		p.mu.Unlock()
		select {
		case out := <-item.done:
			return out.res, out.err
		case <-ctx.Done():
			return PublishResult{}, ctx.Err()
		}
	}
	p.active = true
	p.mu.Unlock()

	//nolint:contextcheck // A coalesced batch must not borrow one waiting caller's actor context.
	go p.drainLoop() // #nosec G118 -- Publication owns queued results beyond caller cancellation.

	select {
	case out := <-item.done:
		return out.res, out.err
	case <-ctx.Done():
		return PublishResult{}, ctx.Err()
	}
}

func (p *Publisher) drainLoop() {
	for {
		p.mu.Lock()
		if len(p.queue) == 0 {
			p.active = false
			p.mu.Unlock()
			return
		}
		batch := p.queue
		p.queue = nil
		p.mu.Unlock()

		res, err := p.executeBatch(context.Background(), batch)
		for _, waiter := range batch {
			waiter.done <- publishOutcome{res: res, err: err}
		}
	}
}

func (p *Publisher) executeBatch(ctx context.Context, batch []*pendingPublish) (res PublishResult, retErr error) {
	merged := mergeBatchRequests(p, batch)
	cb := p.cfg.Callbacks
	inject := func(stage FaultStage) error {
		if merged.FaultInjector != nil {
			if err := merged.FaultInjector(stage); err != nil {
				return err
			}
		}
		if cb.FaultInjector != nil {
			return cb.FaultInjector(stage)
		}
		return nil
	}

	// Step 1: Stop admission of new mutations and acquire adapter/process-supervision barrier.
	if cb.QuiesceWorkers != nil {
		if err := cb.QuiesceWorkers(ctx); err != nil {
			return PublishResult{}, err
		}
	}
	quiesced := true
	defer func() {
		if quiesced && cb.ResumeWorkers != nil {
			cb.ResumeWorkers(ctx, retErr)
		}
	}()

	if err := p.flushRegisteredMappings(); err != nil {
		return PublishResult{}, fmt.Errorf("%w: %w", ErrFlushFailed, err)
	}

	token, err := p.cfg.Core.BeginCheckpointBarrier(ctx)
	if err != nil {
		return PublishResult{}, err
	}
	// Step 6: Release the barrier on EVERY path.
	defer func() {
		_ = p.cfg.Core.EndCheckpointBarrier(token)
	}()

	if err := inject(FaultAfterBarrier); err != nil {
		return PublishResult{}, err
	}

	// Step 2: Flush client dirty data, including writable mappings, through the adapter.
	if err := p.flushClients(ctx, inject); err != nil {
		return PublishResult{}, fmt.Errorf("%w: %w", ErrFlushFailed, err)
	}

	// Step 3 & 4: Enumerate journaled logical changes, persist blobs, enforce limits, stage manifest.
	manifest, retainedBlobs, err := p.captureAndStageManifest(merged, batch, inject)
	if err != nil {
		return PublishResult{}, err
	}

	// Step 5: Atomically publish retention references and the checkpoint manifest.
	if err := p.commitRetentionAndPublish(ctx, manifest, retainedBlobs, inject); err != nil {
		return PublishResult{}, err
	}

	return PublishResult{Manifest: manifest, CoalescedCount: len(batch)}, nil
}

func (p *Publisher) flushRegisteredMappings() error {
	p.mu.Lock()
	mappings := append([]*MappedRegion(nil), p.mappings...)
	p.mu.Unlock()
	for _, m := range mappings {
		if m != nil && m.Flush != nil {
			if err := m.Flush(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *Publisher) flushClients(ctx context.Context, inject func(FaultStage) error) error {
	if err := inject(FaultOnFlush); err != nil {
		return err
	}
	if p.cfg.Callbacks.FlushClientDirtyData != nil {
		if err := p.cfg.Callbacks.FlushClientDirtyData(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (p *Publisher) commitRetentionAndPublish(
	ctx context.Context,
	m Manifest,
	blobs []string,
	inject func(FaultStage) error,
) error {
	if err := inject(FaultOnRetentionCommit); err != nil {
		return fmt.Errorf("%w: %w", ErrRetentionFailed, err)
	}
	rec := RetentionRecord{
		CheckpointID: m.CheckpointID,
		Blobs:        blobs,
		GitObjects:   append([]string(nil), m.PrivateGit.RetainedObjects...),
		CommittedAt:  time.Now().UTC(),
	}
	if err := p.cfg.Store.writeRetentionRecord(rec); err != nil {
		return fmt.Errorf("%w: %w", ErrRetentionFailed, err)
	}
	if p.cfg.Callbacks.CommitRetention != nil {
		if err := p.cfg.Callbacks.CommitRetention(ctx, m.CheckpointID, blobs, rec.GitObjects); err != nil {
			return fmt.Errorf("%w: %w", ErrRetentionFailed, err)
		}
	}
	return p.cfg.Store.commitManifestAndLatest(m.CheckpointID)
}
