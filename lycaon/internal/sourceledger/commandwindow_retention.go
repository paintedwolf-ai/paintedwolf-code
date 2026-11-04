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

// startRetention keeps the before-images a window's admissions may need, bounded
// by its file limit, windowRetainBytes and its budget. A file left unretained loses
// its before-image, not its attribution, so running out of time keeps the window;
// stored objects carry over, so later opens resume cheaply.
type startRetention struct {
	objects   *sourceblob.Store
	queries   *db.Queries
	recordMu  *sync.Mutex
	snapshots *sourcesnapshot.Store
	sqlDB     db.Handle
	// budget is the share of an open spent keeping before-images.
	budget time.Duration
	// files bounds how many before-images one window keeps.
	files int
}

// Retain pins what snapshotID holds to windowID until the window settles.
func (r *startRetention) Retain(ctx context.Context, windowID, snapshotID string) error {
	if snapshotID == "" || r.snapshots == nil {
		return nil
	}
	budget, cancel := context.WithTimeout(ctx, r.budget)
	defer cancel()
	// New objects are reclaimable until pinned, so the lease spans the pin.
	release := r.objects.AcquireReferenceLease()
	defer release()
	pins, err := r.storeObjects(budget, windowID, snapshotID)
	if err != nil && budget.Err() == nil {
		return err
	}
	if err != nil {
		slog.InfoContext(ctx, "command window start retention bounded by time",
			"window_id", windowID, "retained_files", len(pins), "limit", r.budget)
	}
	return r.pin(context.WithoutCancel(ctx), windowID, pins)
}

// storeObjects stores what it can and returns what it stored, including when it
// stops early with an error.
func (r *startRetention) storeObjects(ctx context.Context, windowID, snapshotID string) ([]string, error) {
	entries, err := r.snapshots.HashedEntries(ctx, snapshotID, MaxRevisionContentBytes, r.files+1)
	if err != nil {
		return nil, err
	}
	if len(entries) > r.files {
		slog.InfoContext(ctx, "command window start retention bounded",
			"window_id", windowID, "unretained", len(entries)-r.files, "limit", r.files)
		entries = entries[:r.files]
	}
	var retained int64
	pins := make([]string, 0, len(entries))
	for _, entry := range entries {
		if retained+entry.Size > windowRetainBytes {
			slog.InfoContext(ctx, "command window start retention bounded by size",
				"window_id", windowID, "retained_bytes", retained, "limit", windowRetainBytes)
			break
		}
		if _, err := r.queries.GetSourceBlobObject(ctx, entry.SHA256); err == nil {
			pins = append(pins, entry.SHA256)
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
		retained += int64(len(raw))
		pins = append(pins, entry.SHA256)
	}
	return pins, nil
}

func (r *startRetention) pin(ctx context.Context, windowID string, pins []string) error {
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
	for _, sha := range pins {
		if err := q.InsertSourceCommandWindowObject(ctx, db.InsertSourceCommandWindowObjectParams{WindowID: windowID, Sha256: sha}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
