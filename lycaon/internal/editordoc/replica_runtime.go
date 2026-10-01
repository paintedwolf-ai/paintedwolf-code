package editordoc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/documentcore"
)

const replicaCacheCapacity = 32

// Replica runtime state is speculative until its matching document revision
// commits. A mismatch reloads the accepted checkpoint and journal.
type replicaRuntime struct {
	mu      sync.Mutex
	engine  *documentcore.Engine
	next    uint32
	entries map[string]*replicaEntry
}

type replicaEntry struct {
	documentID string
	undoFloor  int64
	bytes      int
	handle     uint32
	revision   int64
	head       ReplicaHead
	used       time.Time
}

func randomReplicaID(excluded ...uint32) (uint32, error) {
	var bytes [4]byte
	for {
		if _, err := rand.Read(bytes[:]); err != nil {
			return 0, err
		}
		if id := binary.LittleEndian.Uint32(bytes[:]); id != 0 && !slices.Contains(excluded, id) {
			return id, nil
		}
	}
}

// ensureEngine starts the document core, replacing one a trap closed. The
// caller holds the runtime mutex.
func (r *replicaRuntime) ensureEngine(ctx context.Context) error {
	if r.engine != nil && r.engine.Closed() {
		_ = r.engine.Close(context.WithoutCancel(ctx))
		r.engine = nil
		r.entries = nil
	}
	if r.engine == nil {
		engine, err := documentcore.New(ctx)
		if err != nil {
			return err
		}
		r.engine, r.entries = engine, make(map[string]*replicaEntry)
	}
	return nil
}

func (s *Service) loadReplica(ctx context.Context, d *Document) (*replicaEntry, error) {
	r := &s.replicas
	if err := r.ensureEngine(ctx); err != nil {
		return nil, err
	}
	if entry := r.entries[d.ID]; entry != nil && entry.revision == d.Revision {
		entry.used = time.Now()
		return entry, nil
	}
	r.evict(ctx, d.ID)

	head, err := s.store.replicaHead(ctx, d.ID)
	if errors.Is(err, ErrNotFound) {
		return s.initializeReplica(ctx, d)
	}
	if err != nil {
		return nil, err
	}
	r.trim(ctx, d.ID, len(head.Checkpoint))
	r.next++
	entry := &replicaEntry{documentID: d.ID, handle: r.next, revision: d.Revision, head: *head, used: time.Now(), bytes: len(head.Checkpoint)}
	defer func() {
		if r.entries[d.ID] != entry {
			r.drop(context.WithoutCancel(ctx), entry.handle)
		}
	}()
	snapshot, err := r.engine.Call(ctx, documentcore.Request{Action: "open", Handle: entry.handle,
		Client: head.HostClient, Update: head.Checkpoint})
	if err != nil {
		return nil, err
	}
	updates, err := s.store.replicaUpdates(ctx, d.ID, head.CheckpointRevision)
	if err != nil {
		return nil, err
	}
	for _, update := range updates {
		entry.bytes += len(update)
		snapshot, err = r.engine.Call(ctx, documentcore.Request{Action: "apply", Handle: entry.handle, Update: update})
		if err != nil {
			return nil, err
		}
	}
	if snapshot.Text != d.Draft {
		return nil, errors.New("accepted document text does not match its CRDT journal")
	}
	r.entries[d.ID] = entry
	r.trim(ctx, d.ID, 0)
	return entry, nil
}

func (r *replicaRuntime) evict(ctx context.Context, id string) {
	if entry := r.entries[id]; entry != nil {
		r.drop(ctx, entry.handle)
		delete(r.entries, id)
	}
}

func (r *replicaRuntime) drop(ctx context.Context, handle uint32) {
	if r.engine != nil && !r.engine.Closed() {
		_, _ = r.engine.Call(ctx, documentcore.Request{Action: "drop", Handle: handle})
	}
}

func (s *Service) initializeReplica(ctx context.Context, d *Document) (*replicaEntry, error) {
	r := &s.replicas
	client, err := randomReplicaID()
	if err != nil {
		return nil, err
	}
	filesystemClient, err := randomReplicaID(client)
	if err != nil {
		return nil, err
	}
	r.next++
	handle := r.next
	defer func() {
		if entry := r.entries[d.ID]; entry == nil || entry.handle != handle {
			r.drop(context.WithoutCancel(ctx), handle)
		}
	}()
	_, err = r.engine.Call(ctx, documentcore.Request{Action: "open", Handle: r.next, Client: client})
	if err != nil {
		return nil, err
	}
	snapshot, err := r.engine.Call(ctx, documentcore.Request{Action: "edit", Handle: r.next,
		Edits: []documentcore.Edit{{Insert: d.Draft}}, Checkpoint: true})
	if err != nil {
		return nil, err
	}
	entry := &replicaEntry{documentID: d.ID, handle: r.next, revision: d.Revision, used: time.Now(), bytes: len(snapshot.Checkpoint), head: ReplicaHead{
		Epoch: 1, HostClient: client, FilesystemClient: filesystemClient, PublishedCheckpoint: snapshot.Checkpoint, Checkpoint: snapshot.Checkpoint,
		CheckpointRevision: d.Revision, Vector: snapshot.Vector, PublishedRevision: d.Revision}}
	authors, err := s.initialAuthorship(ctx, d, client)
	if err != nil {
		return nil, err
	}
	if err := s.store.Tx(ctx, func(tx *sql.Tx) error {
		if err := insertReplicaHead(ctx, tx, d.ID, &entry.head); err != nil {
			return err
		}
		return recordInitialAuthorship(ctx, tx, authors)
	}); err != nil {
		return nil, err
	}
	r.entries[d.ID] = entry
	r.trim(ctx, d.ID, 0)
	return entry, nil
}

func (s *Service) replicaProjection(ctx context.Context, d *Document, vector []byte) error {
	return s.projectReplica(ctx, d, func(*ReplicaHead) []byte { return vector })
}

// projectReplica chooses the client's vector once the replica head, which owns the epoch, is loaded.
func (s *Service) projectReplica(ctx context.Context, d *Document, vectorFor func(head *ReplicaHead) []byte) error {
	s.projectWorkspacePresentation(ctx, d)

	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, d)
	if err != nil {
		return err
	}
	vector := vectorFor(&entry.head)
	if len(vector) == 0 {
		vector = []byte{0}
	}
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "inspect", OmitText: true, Handle: entry.handle, Vector: vector})
	if err != nil {
		return fmt.Errorf("project document replica: %w", err)
	}
	d.Epoch, d.StateVector, d.CRDTUpdate = entry.head.Epoch, snapshot.Vector, snapshot.Update
	d.PublishedRevision = entry.head.PublishedRevision
	return nil
}

func (s *Service) Close(ctx context.Context) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.closePresence()
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	if s.replicas.engine == nil {
		return nil
	}
	err := s.replicas.engine.Close(ctx)
	s.replicas.engine = nil
	s.replicas.entries = nil
	return err
}
