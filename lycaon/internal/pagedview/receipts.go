package pagedview

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const receiptPayloadBytes = 1024

// Receipts retains bounded, temporary idempotency records per namespace.
type Receipts struct {
	mu     sync.Mutex
	db     *sql.DB
	closed bool
}

type SavedReceipt struct {
	Digest []byte
	Value  []byte
}

func (r *Receipts) openLocked(ctx context.Context) error {
	if r.closed {
		return ErrExpired
	}
	if r.db != nil {
		return nil
	}
	db, err := sql.Open("sqlite", "")
	if err != nil {
		return fmt.Errorf("open receipt journal: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.ExecContext(ctx, `PRAGMA page_size=4096; PRAGMA cache_size=-512; PRAGMA max_page_count=65536;
CREATE TABLE receipts (namespace TEXT NOT NULL, id TEXT NOT NULL, digest BLOB NOT NULL, complete INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(namespace,id)) WITHOUT ROWID;`)
	if err != nil {
		_ = db.Close()
		return fmt.Errorf("initialize receipt journal: %w", err)
	}
	r.db = db
	return nil
}

func (r *Receipts) Lookup(ctx context.Context, namespace, id string) (SavedReceipt, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.openLocked(ctx); err != nil {
		return SavedReceipt{}, false, err
	}
	var saved SavedReceipt
	var complete bool
	err := r.db.QueryRowContext(ctx, `SELECT digest,complete,payload FROM receipts WHERE namespace=? AND id=?`, namespace, id).Scan(&saved.Digest, &complete, &saved.Value)
	if errors.Is(err, sql.ErrNoRows) {
		return SavedReceipt{}, false, nil
	}
	if err != nil {
		return SavedReceipt{}, false, fmt.Errorf("read receipt: %w", err)
	}
	if !complete {
		return SavedReceipt{}, true, ErrExpired
	}
	saved.Value = bytes.TrimRight(saved.Value, "\x00")
	return saved, true, nil
}

// Reserve allocates the complete record before an adapter mutates its state.
func (r *Receipts) Reserve(ctx context.Context, namespace, id string, digest []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := r.openLocked(ctx); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO receipts(namespace,id,digest,complete,payload) VALUES(?,?,?,0,zeroblob(?))`, namespace, id, digest, receiptPayloadBytes)
	if err != nil {
		return fmt.Errorf("reserve receipt: %w", err)
	}
	return nil
}

// Complete outlives request cancellation, with a bounded deadline.
func (r *Receipts) Complete(ctx context.Context, namespace, id string, value []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.openLocked(ctx); err != nil {
		return err
	}
	if len(value) > receiptPayloadBytes {
		return ErrExpired
	}
	payload := make([]byte, receiptPayloadBytes)
	copy(payload, value)
	result, err := r.db.ExecContext(ctx, `UPDATE receipts SET complete=1,payload=? WHERE namespace=? AND id=?`, payload, namespace, id)
	if err != nil {
		// An incomplete reservation expires only this request.
		return fmt.Errorf("complete receipt: %w: %w", ErrExpired, err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return fmt.Errorf("complete missing receipt: %w", ErrExpired)
	}
	return nil
}

func (r *Receipts) Abort(ctx context.Context, namespace, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := r.openLocked(ctx); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM receipts WHERE namespace=? AND id=? AND complete=0`, namespace, id)
	if err != nil {
		return fmt.Errorf("abandon receipt: %w: %w", ErrExpired, err)
	}
	return nil
}

//nolint:contextcheck,nolintlint // Receipt cleanup outlives view cancellation.
func (r *Receipts) Release(namespace string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if r.db == nil || r.closed {
		return nil
	}
	// Failed cleanup retains this namespace and its replay protection.
	_, err := r.db.ExecContext(ctx, `DELETE FROM receipts WHERE namespace=?`, namespace)
	if err != nil {
		return fmt.Errorf("release receipt namespace: %w", err)
	}
	return nil
}

func (r *Receipts) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	if r.db != nil {
		_ = r.db.Close()
		r.db = nil
	}
}
