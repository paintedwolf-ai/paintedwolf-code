package editordoc

import (
	"context"
	"errors"
	"math"

	"github.com/lycaon/lycaon/internal/documentcore"
)

const (
	replicaHistoryBytes = 16 << 20
	replicaCacheBytes   = 24 << 20
	// historyCapacityCode names the rejection a journal past its budget raises.
	historyCapacityCode = "document_history_capacity"
)

// isHistoryCapacity reports the rejection that a new epoch recovers from.
func isHistoryCapacity(err error) bool {
	var rejected *documentcore.Rejected
	return errors.As(err, &rejected) && rejected.Code == historyCapacityCode
}

// accountReplicaSnapshot keeps the serialized history inside its budget:
// compaction spares retained undo first, gives it up second, and a history
// that still does not fit is rejected so the document can start a new epoch.
func (s *Service) accountReplicaSnapshot(ctx context.Context, entry *replicaEntry, snapshot *documentcore.Snapshot) error {
	size := entry.bytes + len(snapshot.Update)
	if len(snapshot.Checkpoint) > 0 {
		size = len(snapshot.Checkpoint)
	}
	if size > replicaHistoryBytes/2 {
		retained, floor, err := s.retainedUndo(ctx, entry.documentID, snapshot)
		if err != nil {
			return err
		}
		compacted, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "compact", OmitText: true, Handle: entry.handle, RetainedUndo: retained, Checkpoint: true})
		if err != nil {
			return err
		}
		snapshot.Checkpoint = compacted.Checkpoint
		entry.undoFloor = floor
		size = len(compacted.Checkpoint)
	}
	if size > replicaHistoryBytes {
		compacted, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "compact", OmitText: true, Handle: entry.handle, Checkpoint: true})
		if err != nil {
			return err
		}
		snapshot.Checkpoint = compacted.Checkpoint
		// Every undo record now refers to collected content.
		entry.undoFloor = math.MaxInt64
		size = len(compacted.Checkpoint)
	}
	if size > replicaHistoryBytes {
		return &documentcore.Rejected{Code: historyCapacityCode}
	}
	entry.bytes = size
	return nil
}

func (s *Service) retainedUndo(ctx context.Context, id string, pending *documentcore.Snapshot) ([][]byte, int64, error) {
	const maxUndoBytes uint64 = 4 << 20
	const maxUndoEntries = 256
	retained := make([][]byte, 0, maxUndoEntries)
	var weight uint64
	if len(pending.Undo) > 0 {
		retained = append(retained, pending.Undo)
		weight = pending.UndoUnits*4 + uint64(len(pending.Undo))
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT revision,undo_bytes,undo_units FROM editor_document_changes WHERE document_id=? AND actor_kind IN ('agent','restore') ORDER BY revision DESC LIMIT 256`, id)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	var floor int64
	for rows.Next() {
		var revision int64
		var undo []byte
		var units uint64
		if err := rows.Scan(&revision, &undo, &units); err != nil {
			return nil, 0, err
		}
		if len(retained) >= maxUndoEntries || weight+units*4+uint64(len(undo)) > maxUndoBytes {
			return retained, revision, nil
		}
		retained = append(retained, undo)
		weight += units*4 + uint64(len(undo))
		floor = revision - 1
	}
	return retained, floor, rows.Err()
}

func (r *replicaRuntime) trim(ctx context.Context, keep string, incoming int) {
	for {
		bytes, oldest := incoming, ""
		for id, entry := range r.entries {
			bytes += entry.bytes
			if id != keep && (oldest == "" || entry.used.Before(r.entries[oldest].used)) {
				oldest = id
			}
		}
		if oldest == "" || (bytes <= replicaCacheBytes && len(r.entries) < replicaCacheCapacity) {
			return
		}
		r.evict(ctx, oldest)
	}
}
