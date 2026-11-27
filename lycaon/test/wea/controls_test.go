// Package wea_test implements the D3 in-process lane control tests for TB02.
// This file is strictly test-only (*_test.go) and is never linked into any shipped binary.
package wea_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func gitRev(t *testing.T, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func countChatGrantsRO(t *testing.T, ctx context.Context, dbPath, grantID string) int {
	t.Helper()
	ro, err := db.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatalf("db.OpenReadOnly failed: %v", err)
	}
	defer ro.Close()
	var n int
	if err := ro.QueryRowContext(ctx, "SELECT COUNT(*) FROM chat_grants WHERE id = ?", grantID).Scan(&n); err != nil {
		t.Fatalf("read-only chat_grants query failed: %v", err)
	}
	return n
}

func TestInProcessLaneChatGrantExpiryDependsOnManualClockAndBarrier(t *testing.T) {
	ctx := t.Context()
	overlaySHA := testOverlayDigest(t)
	if len(overlaySHA) != 64 {
		t.Fatalf("unexpected overlay digest length: %d", len(overlaySHA))
	}

	// Open the real migrated SQLite store in an isolated temporary profile.
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
	t0 := clk.Now()
	expiresAt := t0.Add(10 * time.Minute)
	nowStr := t0.Format(time.RFC3339)

	// Read the migrated owner person ID and seed a real project + session to satisfy chat_grants foreign keys.
	var ownerPersonID string
	if err := store.QueryRowContext(ctx, `SELECT id FROM people WHERE role = 'owner' LIMIT 1`).Scan(&ownerPersonID); err != nil {
		t.Fatalf("read owner person: %v", err)
	}
	if _, err := store.ExecContext(ctx,
		`INSERT INTO projects (id, name, last_opened_at, created_at) VALUES ('proj-wea', 'WEA Fixture', ?, ?)`,
		nowStr, nowStr,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := store.ExecContext(ctx,
		`INSERT INTO sessions (id, project_id, owner_person_id, root_session_id, mode, posture, created_at, updated_at, activity_at)
		 VALUES ('sess-wea-inproc', 'proj-wea', ?, 'sess-wea-inproc', 'agent', 'build', ?, ?, ?)`,
		ownerPersonID, nowStr, nowStr, nowStr,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	q := db.New(store)
	const grantID = "grant-wea-expiring-001"
	if err := q.UpsertChatGrant(ctx, db.UpsertChatGrantParams{
		ChatSessionID: "sess-wea-inproc",
		ID:            grantID,
		CheckpointID:  "cp-wea-001",
		DeltaJson:     `{"kind":"lease","scope":"chat"}`,
		CreatedAt:     nowStr,
		ExpiresAt:     sql.NullString{String: expiresAt.Format(time.RFC3339), Valid: true},
	}); err != nil {
		t.Fatalf("UpsertChatGrant failed: %v", err)
	}

	// Phase 1: Run DeleteExpiredChatGrants at t0 (before expiry) -> grant MUST remain persisted.
	if err := q.DeleteExpiredChatGrants(ctx, sql.NullString{String: clk.Now().Format(time.RFC3339), Valid: true}); err != nil {
		t.Fatalf("DeleteExpiredChatGrants(t0) failed: %v", err)
	}
	preCount := countChatGrantsRO(t, ctx, dbPath, grantID)
	if preCount != 1 {
		t.Fatalf("expected grant to persist before expiry at %s, got count=%d", t0.Format(time.RFC3339), preCount)
	}

	// Phase 2: Hold the expiry sweep on a deterministic barrier, advance manual clock past expiresAt,
	// release the barrier, and verify the real store commits the deletion.
	gate := newBarrier()
	errCh := make(chan error, 1)
	go func() {
		if err := gate.Wait(ctx); err != nil {
			errCh <- err
			return
		}
		errCh <- q.DeleteExpiredChatGrants(ctx, sql.NullString{String: clk.Now().Format(time.RFC3339), Valid: true})
	}()

	<-gate.entered
	t1 := clk.Advance(15 * time.Minute)
	gate.Release()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("DeleteExpiredChatGrants(t1) failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for barrier-gated expiry sweep")
	}

	postCount := countChatGrantsRO(t, ctx, dbPath, grantID)
	if postCount != 0 {
		t.Fatalf("expected grant to expire after clock advanced to %s, got count=%d", t1.Format(time.RFC3339), postCount)
	}

	receipt := map[string]any{
		"schema":                 "wea-inproc-control-v1",
		"lane":                   "in-process",
		"product_git_head":       gitRev(t, "HEAD"),
		"product_git_tree":       gitRev(t, "HEAD^{tree}"),
		"test_overlay_digest":    overlaySHA,
		"grant_id":               grantID,
		"manual_clock_t0":        t0.Format(time.RFC3339),
		"expires_at":             expiresAt.Format(time.RFC3339),
		"manual_clock_t1":        t1.Format(time.RFC3339),
		"pre_expiry_grant_count": preCount,
		"post_expiry_grant_count": postCount,
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	t.Logf("WEA_INPROC_RECEIPT:%s", encoded)
}
