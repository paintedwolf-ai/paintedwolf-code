package sourceledger

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

// Retain before-images unavailable from repository objects or history until the window settles.
const (
	windowRetainFiles = 8192
	windowRetainBytes = 256 << 20
)

// retainedSet is what one window's before-images hold, shared by every folder
// the window covers so its limits bound the window, not each folder.
type retainedSet struct {
	mu    sync.Mutex
	shas  map[string]struct{}
	bytes int64
}

func newRetainedSet() *retainedSet {
	return &retainedSet{shas: make(map[string]struct{})}
}

// startRetention keeps the before-images a window's admissions may need, bounded
// by its file and byte limits across all the window's folders and by its budget.
// A file left unretained loses its before-image, not its attribution, so
// running out of time keeps the window; stored objects carry over, so later
// opens resume cheaply.
type startRetention struct {
	objects   *sourceblob.Store
	queries   *db.Queries
	recordMu  *sync.Mutex
	snapshots *sourcesnapshot.Store
	sqlDB     db.Handle
	// budget is the share of an open spent keeping before-images.
	budget time.Duration
	// files and bytes bound what one window keeps.
	files int
	bytes int64
}

// retainedObject is one before-image a call pins.
type retainedObject struct {
	sha  string
	size int64
}

// Retain pins what the snapshots hold to windowID until the window settles,
// within what held leaves of the window's limits.
func (r *startRetention) Retain(ctx context.Context, windowID string, held *retainedSet, snapshotIDs ...string) error {
	if r.snapshots == nil || held == nil {
		return nil
	}
	held.mu.Lock()
	defer held.mu.Unlock()
	budget, cancel := context.WithTimeout(ctx, r.budget)
	defer cancel()
	// New objects are reclaimable until pinned, so the lease spans the pin.
	release := r.objects.AcquireReferenceLease()
	defer release()
	var pins []retainedObject
	var err error
	for _, snapshotID := range snapshotIDs {
		if snapshotID == "" {
			continue
		}
		if pins, err = r.storeObjects(budget, windowID, snapshotID, held, pins); err != nil {
			break
		}
	}
	if err != nil && budget.Err() == nil {
		return err
	}
	if err != nil {
		slog.InfoContext(ctx, "command window start retention bounded by time",
			"window_id", windowID, "retained_files", len(pins), "limit", r.budget)
	}
	if err := r.pin(context.WithoutCancel(ctx), windowID, pins); err != nil {
		return err
	}
	for _, pin := range pins {
		held.shas[pin.sha] = struct{}{}
		held.bytes += pin.size
	}
	return nil
}

// storeObjects adds to pins what it stores, including when it stops early with
// an error. Objects held already count once.
func (r *startRetention) storeObjects(ctx context.Context, windowID, snapshotID string, held *retainedSet, pins []retainedObject) ([]retainedObject, error) {
	entries, err := r.snapshots.HashedEntries(ctx, snapshotID, sourceblob.MaxRevisionContentBytes, r.files+1)
	if err != nil {
		return pins, err
	}
	files, bytes := len(held.shas)+len(pins), held.bytes
	for _, pin := range pins {
		bytes += pin.size
	}
	chosen := make(map[string]struct{}, len(pins))
	for _, pin := range pins {
		chosen[pin.sha] = struct{}{}
	}
	for _, entry := range entries {
		if _, ok := held.shas[entry.SHA256]; ok {
			continue
		}
		if _, ok := chosen[entry.SHA256]; ok {
			continue
		}
		if files >= r.files {
			slog.InfoContext(ctx, "command window start retention bounded",
				"window_id", windowID, "limit", r.files)
			return pins, nil
		}
		if bytes+entry.Size > r.bytes {
			slog.InfoContext(ctx, "command window start retention bounded by size",
				"window_id", windowID, "retained_bytes", bytes, "limit", r.bytes)
			return pins, nil
		}
		if _, err := r.queries.GetSourceBlobObject(ctx, entry.SHA256); err == nil {
			pins = append(pins, retainedObject{sha: entry.SHA256, size: entry.Size})
			chosen[entry.SHA256] = struct{}{}
			files, bytes = files+1, bytes+entry.Size
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			return pins, err
		}
		raw, err := r.snapshots.Bytes(ctx, entry)
		if errors.Is(err, sourcesnapshot.ErrContentUnavailable) {
			// Already moved on, or out of time; the pass that admits it records what it can.
			if ctx.Err() != nil {
				return pins, ctx.Err()
			}
			continue
		}
		if err != nil {
			return pins, err
		}
		rel, stored, oids, err := r.objects.Put(ctx, entry.SHA256, raw)
		if err != nil {
			return pins, err
		}
		if err := r.queries.UpsertSourceBlobObject(ctx, db.UpsertSourceBlobObjectParams{
			Sha256: entry.SHA256, Size: int64(len(raw)), StoredSize: stored, StorageRelpath: rel,
			GitOidSha1: oids.SHA1, GitOidSha256: oids.SHA256,
		}); err != nil {
			return pins, err
		}
		pins = append(pins, retainedObject{sha: entry.SHA256, size: int64(len(raw))})
		chosen[entry.SHA256] = struct{}{}
		files, bytes = files+1, bytes+int64(len(raw))
	}
	return pins, nil
}

func (r *startRetention) pin(ctx context.Context, windowID string, pins []retainedObject) error {
	if len(pins) == 0 {
		return nil
	}
	r.recordMu.Lock()
	defer r.recordMu.Unlock()
	tx, err := r.sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	for _, pin := range pins {
		if err := q.InsertSourceCommandWindowObject(ctx, db.InsertSourceCommandWindowObjectParams{WindowID: windowID, Sha256: pin.sha}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
