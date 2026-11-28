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
	"github.com/lycaon/lycaon/internal/presence"
)

// manualClock provides deterministic time control for in-process service expiry checks (D3).
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

func readVaultUnlockEndReasonRO(t *testing.T, ctx context.Context, dbPath, unlockID string) (string, bool) {
	t.Helper()
	ro, err := db.OpenReadOnly(ctx, dbPath)
	if err != nil {
		t.Fatalf("db.OpenReadOnly failed: %v", err)
	}
	defer ro.Close()
	var reason sql.NullString
	err = ro.QueryRowContext(ctx, "SELECT end_reason FROM vault_unlocks WHERE id = ?", unlockID).Scan(&reason)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("read-only vault_unlocks query failed: %v", err)
	}
	return reason.String, true
}

// TestInProcessLanePresenceServiceExpiryWithManualClockAndBarrier exercises the real
// `internal/presence` Unlocks service using its existing injected `SetClock` hook,
// persists its unlock-ended callback to the real migrated SQLite `vault_unlocks` table,
// and independently verifies both pre- and post-expiry states via `db.OpenReadOnly`.
func TestInProcessLanePresenceServiceExpiryWithManualClockAndBarrier(t *testing.T) {
	ctx := t.Context()
	overlaySHA := testOverlayDigest(t)
	if len(overlaySHA) != 64 {
		t.Fatalf("unexpected overlay digest length: %d", len(overlaySHA))
	}

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
	nowStr := t0.Format(time.RFC3339)

	// Read migrated owner person ID and seed a real project + session for vault_unlocks FKs.
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
	const chatSessionID = "sess-wea-inproc"
	if _, err := store.ExecContext(ctx,
		`INSERT INTO sessions (id, project_id, owner_person_id, root_session_id, mode, posture, created_at, updated_at, activity_at)
		 VALUES (?, 'proj-wea', ?, ?, 'agent', 'build', ?, ?, ?)`,
		chatSessionID, ownerPersonID, chatSessionID, nowStr, nowStr, nowStr,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Construct the real product presence.Broker + presence.Unlocks service with injected manual clock.
	broker := presence.NewBroker()
	broker.SetClock(clk.Now)
	unlocks := presence.NewUnlocks(broker)
	unlocks.SetClock(clk.Now)

	const unlockID = "11111111-2222-3333-4444-555555555555"
	const tsFmt = "2006-01-02T15:04:05.000000000Z"
	var persistErr error
	unlocks.SetObserver(presence.UnlockObserver{
		Ended: func(u presence.Unlock, reason presence.EndReason, endedAt time.Time) {
			_, persistErr = store.ExecContext(ctx,
				`INSERT INTO vault_unlocks (id, project_id, chat_session_id, person_id, authenticator, window_label, unlocked_at, ended_at, end_reason)
				 VALUES (?, 'proj-wea', ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(id) DO UPDATE SET ended_at = excluded.ended_at, end_reason = excluded.end_reason`,
				u.ID, u.ChatSessionID, u.PersonID, u.Authenticator, u.WindowLabel,
				u.UnlockedAt.UTC().Format(tsFmt), endedAt.UTC().Format(tsFmt), string(reason),
			)
		},
	})

	opened := presence.NewUnlock(chatSessionID, presence.Verified{
		ChallengeID:   unlockID,
		PersonID:      ownerPersonID,
		Authenticator: presence.AuthenticatorMacOS,
		WindowLabel:   "wea-inproc-window",
		VerifiedAt:    t0,
	})
	if !unlocks.Open(opened) {
		t.Fatalf("presence.Unlocks.Open returned false")
	}

	// Phase 1: Advance just shy of UnlockIdle -> service MUST report unlock still active, 0 ended rows in DB.
	clk.Advance(presence.UnlockIdle - time.Second)
	if _, active := unlocks.Active(chatSessionID); !active {
		t.Fatalf("expected presence unlock to stay active before UnlockIdle")
	}
	if _, found := readVaultUnlockEndReasonRO(t, ctx, dbPath, unlockID); found {
		t.Fatalf("expected no ended vault_unlocks row before idle expiry")
	}

	// Phase 2: Hold Active check behind deterministic barrier, advance clock past UnlockIdle,
	// release barrier, and verify the real presence service expires the unlock as EndIdle
	// and persists the outcome to the real SQLite store.
	gate := newBarrier()
	activeAfterCh := make(chan bool, 1)
	go func() {
		if err := gate.Wait(ctx); err != nil {
			return
		}
		_, ok := unlocks.Active(chatSessionID)
		activeAfterCh <- ok
	}()

	<-gate.entered
	t1 := clk.Advance(2 * time.Second)
	gate.Release()

	select {
	case active := <-activeAfterCh:
		if active {
			t.Fatalf("expected presence unlock to lapse after advancing past UnlockIdle")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for barrier-gated presence service check")
	}

	if persistErr != nil {
		t.Fatalf("persisting ended vault_unlock failed: %v", persistErr)
	}
	endReason, found := readVaultUnlockEndReasonRO(t, ctx, dbPath, unlockID)
	if !found || endReason != string(presence.EndIdle) {
		t.Fatalf("read-only vault_unlocks mismatch: found=%v end_reason=%q want=%q", found, endReason, presence.EndIdle)
	}

	receipt := map[string]any{
		"schema":              "wea-inproc-control-v1",
		"lane":                "in-process",
		"service":             "internal/presence.Unlocks",
		"product_git_head":    gitRev(t, "HEAD"),
		"product_git_tree":    gitRev(t, "HEAD^{tree}"),
		"test_overlay_digest": overlaySHA,
		"unlock_id":           unlockID,
		"manual_clock_t0":     t0.Format(time.RFC3339),
		"manual_clock_t1":     t1.Format(time.RFC3339),
		"pre_expiry_active":   true,
		"post_expiry_active":  false,
		"persisted_end_reason": endReason,
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	t.Logf("WEA_INPROC_RECEIPT:%s", encoded)
}
