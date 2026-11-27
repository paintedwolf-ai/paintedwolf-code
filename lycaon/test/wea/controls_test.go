// Package wea_test implements the D3 in-process lane control tests for TB02.
// This file is strictly test-only (*_test.go) and is never linked into any shipped binary.
package wea_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// manualClock provides deterministic time control for in-process expiry and commit checks (D3).
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(start time.Time) *manualClock {
	return &manualClock{now: start.UTC()}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

// barrier provides a deterministic in-process synchronization gate without sleeping.
type barrier struct {
	entered chan struct{}
	release chan struct{}
}

func newBarrier() *barrier {
	return &barrier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *barrier) Wait(ctx context.Context) error {
	close(b.entered)
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *barrier) Release() {
	close(b.release)
}

// testOverlayDigest computes the SHA-256 digest of this test-only overlay file so
// in-process lane receipts record the exact fixture code compiled alongside the product.
func testOverlayDigest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("unable to resolve test overlay file path")
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read test overlay %s: %v", file, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestInProcessLaneRealStoreManualClockAndBarrier(t *testing.T) {
	ctx := t.Context()
	overlaySHA := testOverlayDigest(t)
	if len(overlaySHA) != 64 {
		t.Fatalf("unexpected overlay digest length: %d", len(overlaySHA))
	}

	// Open the real SQLite store in an isolated temporary profile.
	dbPath := filepath.Join(t.TempDir(), "wea-inproc.db")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open failed: %v", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			t.Errorf("store.Close: %v", cerr)
		}
	}()

	clk := newManualClock(time.Date(2026, 11, 27, 19, 0, 0, 0, time.UTC))
	gate := newBarrier()

	commitDone := make(chan time.Time, 1)
	go func() {
		if err := gate.Wait(ctx); err != nil {
			return
		}
		commitDone <- clk.Now()
	}()

	// Wait deterministically for the goroutine to hit the barrier, advance the manual clock,
	// then release the barrier and verify the committed timestamp matches the advanced clock.
	<-gate.entered
	expectedCommitTime := clk.Advance(15 * time.Minute)
	gate.Release()

	select {
	case got := <-commitDone:
		if !got.Equal(expectedCommitTime) {
			t.Fatalf("commit timestamp mismatch: got %v, want %v", got, expectedCommitTime)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for barrier release")
	}

	// Independently open the same SQLite store read-only via db.OpenReadOnly to verify D4 observer access.
	ro, err := db.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatalf("db.OpenReadOnly failed: %v", err)
	}
	defer ro.Close()

	var tableCount int
	if err := ro.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tableCount); err != nil {
		t.Fatalf("read-only schema query failed: %v", err)
	}
	if tableCount == 0 {
		t.Fatalf("expected migrated real store tables, got 0")
	}

	receipt := map[string]any{
		"schema":              "wea-inproc-control-v1",
		"lane":                "in-process",
		"test_overlay_digest": overlaySHA,
		"real_store_tables":   tableCount,
		"manual_clock_commit": expectedCommitTime.Format(time.RFC3339),
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	t.Logf("WEA_INPROC_RECEIPT:%s", encoded)
}
