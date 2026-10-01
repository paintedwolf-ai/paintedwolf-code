package editordoc

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/documentcore"
)

// recoverHistoryCapacity starts a new epoch after a transition was refused
// for history capacity. The refusal still reaches the caller; the replica
// synchronizes and finds the new epoch. The caller holds the document lock.
func (s *Service) recoverHistoryCapacity(ctx context.Context, id string, cause error) {
	if !isHistoryCapacity(cause) {
		return
	}
	if err := s.rebaseEpoch(ctx, id); err != nil {
		slog.WarnContext(ctx, "editor document epoch rebase failed", "document_id", id, "err", err)
	}
}

// rebaseEpoch rebuilds the CRDT history as the saved base plus the current
// draft. Character identities, semantic undo, and the journal of the old
// epoch end here.
func (s *Service) rebaseEpoch(ctx context.Context, id string) error {
	d, err := s.store.Get(ctx, id)
	if err != nil {
		return err
	}
	head, err := s.store.replicaHead(ctx, id)
	if err != nil {
		return err
	}
	r := &s.replicas
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evict(ctx, id)
	if err := r.ensureEngine(ctx); err != nil {
		return err
	}
	hostClient, err := randomReplicaID()
	if err != nil {
		return err
	}
	filesystemClient, err := randomReplicaID(hostClient)
	if err != nil {
		return err
	}
	r.next++
	handle := r.next
	entry := &replicaEntry{documentID: id, handle: handle, used: time.Now()}
	defer func() {
		if r.entries[id] != entry {
			r.drop(context.WithoutCancel(ctx), handle)
		}
	}()
	if _, err := r.engine.Call(ctx, documentcore.Request{Action: "open", Handle: handle, Client: hostClient}); err != nil {
		return err
	}
	// The saved base becomes the filesystem branch; the draft sits on top of it.
	published, err := r.engine.Call(ctx, documentcore.Request{Action: "edit", Handle: handle,
		Edits: []documentcore.Edit{{Insert: d.BaseContent}}, Checkpoint: true})
	if err != nil {
		return err
	}
	snapshot := published
	if d.Draft != d.BaseContent {
		snapshot, err = r.engine.Call(ctx, documentcore.Request{Action: "edit", Handle: handle,
			Edits: documentcore.TextEdits(d.BaseContent, d.Draft), Checkpoint: true})
		if err != nil {
			return err
		}
	}
	if len(snapshot.Checkpoint) > replicaHistoryBytes {
		return &documentcore.Rejected{Code: historyCapacityCode}
	}
	revision := d.Revision + 1
	entry.revision = revision
	entry.bytes = len(snapshot.Checkpoint)
	entry.head = ReplicaHead{
		Epoch: head.Epoch + 1, HostClient: hostClient, FilesystemClient: filesystemClient,
		PublishedCheckpoint: published.Checkpoint, Checkpoint: snapshot.Checkpoint,
		CheckpointRevision: revision, Vector: snapshot.Vector, PublishedRevision: revision,
	}
	now := time.Now().UTC()
	err = s.store.Tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE editor_documents SET revision=?, updated_at=? WHERE id=? AND revision=?`, revision, now.Format(time.RFC3339Nano), id, d.Revision)
		if err != nil {
			return err
		}
		if n, err := result.RowsAffected(); err != nil {
			return err
		} else if n != 1 {
			return ErrRevisionConflict
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM editor_replica_updates WHERE document_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM editor_document_changes WHERE document_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM editor_document_snapshots WHERE document_id=?
			AND NOT EXISTS (SELECT 1 FROM editor_save_pins p WHERE p.document_id=editor_document_snapshots.document_id AND p.revision=editor_document_snapshots.revision)`, id); err != nil {
			return err
		}
		h := entry.head
		_, err = tx.ExecContext(ctx, `UPDATE editor_replica_heads SET epoch=?, host_client=?, filesystem_client=?, published_checkpoint=?,
			checkpoint=?, checkpoint_revision=?, vector=?, published_revision=? WHERE document_id=?`,
			h.Epoch, h.HostClient, h.FilesystemClient, h.PublishedCheckpoint, h.Checkpoint, h.CheckpointRevision, h.Vector, h.PublishedRevision, id)
		return err
	})
	if err != nil {
		return err
	}
	r.entries[id] = entry
	r.trim(ctx, id, 0)
	d.Revision, d.UpdatedAt = revision, now
	d.Epoch, d.StateVector, d.CRDTUpdate, d.PublishedRevision = entry.head.Epoch, snapshot.Vector, snapshot.Checkpoint, revision
	s.changed(ctx, d, true)
	return nil
}
