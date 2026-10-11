package editordoc

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/textfile"
)

// Filesystem edits use the published state, preserving unseen concurrent text.
func (s *Service) importPublishedText(ctx context.Context, d *Document, read *projectsource.SourceReadResult, actor textActor) error {
	return s.importPublishedReplica(ctx, d, read, actor, nil)
}

// A retained semantic checkpoint preserves character identities across rewind.
func (s *Service) importPublishedReplica(ctx context.Context, d *Document, read *projectsource.SourceReadResult, actor textActor, checkpoint []byte) error {
	staged, err := s.stagePublishedReplica(ctx, d, read, actor, checkpoint)
	if err != nil {
		return err
	}
	err = s.store.UpdateCAS(ctx, &staged.next, d.Revision)
	s.settleStagedReplica(ctx, staged, err == nil)
	if err != nil {
		return err
	}
	*d = staged.next
	return nil
}

// stagedReplica is a filesystem import applied to the live replica but not yet durable.
type stagedReplica struct {
	entry     *replicaEntry
	next      Document
	published filesystemBranch
	snapshot  documentcore.Snapshot
}

// stagePublishedReplica applies published bytes to the live replica. The caller
// persists next and then settles the stage, which evicts the replica unless the write committed.
func (s *Service) stagePublishedReplica(ctx context.Context, d *Document, read *projectsource.SourceReadResult, actor textActor, checkpoint []byte) (*stagedReplica, error) {
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, d)
	if err != nil {
		return nil, err
	}
	published := filesystemBranch{Snapshot: documentcore.Snapshot{Update: checkpoint, Checkpoint: checkpoint}, clientID: entry.head.FilesystemClient}
	if len(checkpoint) == 0 {
		published, err = s.filesystemUpdate(ctx, entry, d.BaseContent, read.Content)
		if err != nil {
			return nil, err
		}
	}
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "apply", Handle: entry.handle, Update: published.Update, TrackChanges: true,
		Checkpoint: d.Revision+1-entry.head.CheckpointRevision >= 64})
	if err != nil {
		s.replicas.evict(ctx, d.ID)
		return nil, err
	}
	if len(checkpoint) == 0 {
		snapshot.Inserted, snapshot.Deleted = published.Inserted, published.Deleted
	}
	content, eol, mixed := normalizeEOL(read.Content)
	next := *d
	next.Draft, next.Encoding = snapshot.Text, read.Encoding
	if d.EOL == d.BaseEOL && d.MixedEOL == d.BaseMixedEOL {
		next.EOL, next.MixedEOL = eol, mixed
	}
	if _, err := textfile.EncodeBounded(serializeEOL(next.Draft, next.EOL), next.Encoding, textfile.LimitsForRaw(projectsource.SourceWriteMaxBytes)); err != nil {
		s.replicas.evict(ctx, d.ID)
		return nil, err
	}
	if err := s.accountReplicaSnapshot(ctx, entry, &snapshot); err != nil {
		s.replicas.evict(ctx, d.ID)
		return nil, err
	}
	next.BaseContent, next.BaseSHA256, next.SizeBytes = content, read.SHA256, read.SizeBytes
	next.BaseEOL, next.BaseMixedEOL = eol, mixed
	next.Dirty = next.Draft != content || next.EOL != eol || next.MixedEOL != mixed
	next.Diverged = false
	if !next.Dirty {
		next.HeldAgentVersionID = ""
	}
	next.Revision++
	next.UpdatedAt = time.Now().UTC()
	next.Epoch, next.CRDTUpdate, next.StateVector = entry.head.Epoch, published.Update, snapshot.Vector
	next.PublishedRevision = entry.head.PublishedRevision
	if !next.Dirty {
		next.PublishedRevision = next.Revision
	}
	next.publishedCheckpoint = published.Checkpoint
	next.filesystemClient = published.clientID
	next.replicaCommit = &replicaCommit{undoUnits: snapshot.UndoUnits, undoFloor: entry.undoFloor, revision: next.Revision, epoch: entry.head.Epoch, operationID: uuid.NewString(), actor: actor,
		update: published.Update, vector: snapshot.Vector, checkpoint: snapshot.Checkpoint}
	next.replicaCommit.contribution = textContribution(&next, &snapshot, actor, next.replicaCommit.operationID)
	return &stagedReplica{entry: entry, next: next, published: published, snapshot: snapshot}, nil
}

// settleStagedReplica advances the replica head after a durable write, or discards the live import.
func (s *Service) settleStagedReplica(ctx context.Context, staged *stagedReplica, committed bool) {
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	if !committed {
		s.replicas.evict(ctx, staged.next.ID)
		return
	}
	entry, next := staged.entry, &staged.next
	entry.revision, entry.head.Vector = next.Revision, staged.snapshot.Vector
	entry.head.PublishedCheckpoint, entry.head.PublishedRevision = staged.published.Checkpoint, next.PublishedRevision
	entry.head.FilesystemClient = staged.published.clientID
	if len(staged.snapshot.Checkpoint) > 0 {
		entry.head.CheckpointRevision = next.Revision
	}
	s.replicas.trim(ctx, next.ID, 0)
}

type filesystemBranch struct {
	documentcore.Snapshot
	clientID uint32
}

func (s *Service) filesystemUpdate(ctx context.Context, entry *replicaEntry, base, content string) (filesystemBranch, error) {
	s.replicas.next++
	handle := s.replicas.next
	defer s.replicas.drop(context.WithoutCancel(ctx), handle)
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "open", Handle: handle, Client: entry.head.FilesystemClient, Update: entry.head.PublishedCheckpoint})
	if err != nil {
		return filesystemBranch{}, err
	}
	if snapshot.Text != base {
		return filesystemBranch{}, errors.New("published document text does not match its CRDT checkpoint")
	}
	client, err := s.filesystemAuthor(ctx, entry, snapshot.Vector)
	if err != nil {
		return filesystemBranch{}, err
	}
	if client != entry.head.FilesystemClient {
		_, err = s.replicas.engine.Call(ctx, documentcore.Request{Action: "open", Handle: handle, Client: client, Update: entry.head.PublishedCheckpoint})
		if err != nil {
			return filesystemBranch{}, err
		}
	}
	normalized, _, _ := normalizeEOL(content)
	edits, err := documentcore.ReplacementEdits(base, normalized)
	if err != nil {
		return filesystemBranch{}, err
	}
	snapshot, err = s.replicas.engine.Call(ctx, documentcore.Request{Action: "edit", Handle: handle, Edits: edits, CaptureUndo: true, Checkpoint: true})
	if err != nil {
		return filesystemBranch{}, err
	}
	if len(snapshot.Checkpoint) > replicaHistoryBytes/2 {
		// The saved branch is where semantic rewind undoes agent operations, so
		// it keeps the same deleted content the live document retains for them.
		retained, _, err := s.retainedUndo(ctx, entry.documentID, &snapshot)
		if err != nil {
			return filesystemBranch{}, err
		}
		compacted, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "compact", Handle: handle, OmitText: true, RetainedUndo: retained, Checkpoint: true})
		if err != nil {
			return filesystemBranch{}, err
		}
		snapshot.Checkpoint = compacted.Checkpoint
	}
	if len(snapshot.Checkpoint) > replicaHistoryBytes {
		return filesystemBranch{}, &documentcore.Rejected{Code: "document_history_capacity"}
	}
	return filesystemBranch{Snapshot: snapshot, clientID: client}, nil
}

func (s *Service) filesystemAuthor(ctx context.Context, entry *replicaEntry, publishedVector []byte) (uint32, error) {
	published, err := documentcore.VectorClocks(publishedVector)
	if err != nil {
		return 0, err
	}
	accepted, err := documentcore.VectorClocks(entry.head.Vector)
	if err != nil {
		return 0, err
	}
	client := entry.head.FilesystemClient
	if published[client] >= accepted[client] {
		return client, nil
	}
	// A rewound disk branch needs a fresh author clock.
	return s.registerReplica(ctx, entry.documentID, "filesystem", "filesystem", uuid.NewString(), "")
}

func (s *Service) publicationCheckpoint(ctx context.Context, d *Document) ([]byte, error) {
	if len(d.pinnedCheckpoint) > 0 {
		return d.pinnedCheckpoint, nil
	}
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, d)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "inspect", Handle: entry.handle, Checkpoint: true})
	if err != nil {
		return nil, err
	}
	if snapshot.Text != d.Draft {
		return nil, errors.New("publication text does not match its CRDT checkpoint")
	}
	return snapshot.Checkpoint, nil
}
