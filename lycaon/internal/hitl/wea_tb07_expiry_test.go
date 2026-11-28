// Package hitl contains the TB07 package-internal manual-clock expiry test seam (A02 / D3).
// Strictly test-only (*_test.go), never linked into any shipped binary.
package hitl

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
)

type tb07ManualClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *tb07ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *tb07ManualClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

// TestWEATB07_A02_ManualClockChatGrantRebaseAndExpiryPrune exercises ChatGrant.rebased(now)
// and SQLStore.eachChatGrant(ctx, nil, now, ...) under a deterministic manual clock:
//   - Before deadline: grant is live and its rebased TTL reflects exact remaining seconds.
//   - After deadline: grant is not live, skipped by replay, and pruned from chat_grants.
func TestWEATB07_A02_ManualClockChatGrantRebaseAndExpiryPrune(t *testing.T) {
	ctx := t.Context()
	sqlDB := testdbfixture.Open(t, "tb07-hitl-expiry.db")
	clk := &tb07ManualClock{now: time.Date(2026, 11, 27, 21, 0, 0, 0, time.UTC)}
	t0 := clk.Now()

	// Seed project + session for FK constraints.
	var ownerID string
	if err := sqlDB.QueryRowContext(ctx, `SELECT id FROM people WHERE role = 'owner' LIMIT 1`).Scan(&ownerID); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	nowStr := t0.Format(time.RFC3339)
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO projects (id, name, last_opened_at, created_at) VALUES ('proj-exp', 'Exp', ?, ?)`, nowStr, nowStr,
	); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx,
		`INSERT INTO sessions (id, project_id, owner_person_id, root_session_id, mode, posture, created_at, updated_at, activity_at)
		 VALUES ('sess-exp', 'proj-exp', ?, 'sess-exp', 'agent', 'build', ?, ?, ?)`,
		ownerID, nowStr, nowStr, nowStr,
	); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	// Record a 24h day-rung chat grant via recordChatGrantsTx at manual clock t0.
	dayGrant := ApprovalGrant{
		ID:                "grant-day-exp",
		Scope:             ApprovalGrantScopeChat,
		ChatSessionID:     "sess-exp",
		ProjectID:         "proj-exp",
		TTLSeconds:        DayRungTTLSeconds,
		GrantedByPersonID: ownerID,
	}
	opt := ApprovalOption{
		ID:        "day",
		Authority: []ApprovalAuthorityDelta{{Kind: AuthorityGenericGrant, Grant: &dayGrant, TTLSeconds: DayRungTTLSeconds}},
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if err := recordChatGrantsTx(ctx, tx, "cp-exp-1", opt, approvedAuthority{display: opt}, t0); err != nil {
		_ = tx.Rollback()
		t.Fatalf("recordChatGrantsTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	store := NewSQLStore(sqlDB)

	// Phase 1: Advance to 10 seconds before 24h expiry -> grant MUST be live with remaining TTL == 10s.
	tBefore := clk.Advance(24*time.Hour - 10*time.Second)
	var seenBefore []ChatGrant
	if err := store.eachChatGrant(ctx, nil, tBefore, func(g ChatGrant) error {
		seenBefore = append(seenBefore, g)
		return nil
	}); err != nil {
		t.Fatalf("eachChatGrant before expiry: %v", err)
	}
	if len(seenBefore) != 1 {
		t.Fatalf("expected 1 live chat grant before expiry, got %d", len(seenBefore))
	}
	rebased, live := seenBefore[0].rebased(tBefore)
	if !live || rebased.TTLSeconds != 10 || rebased.Grant == nil || rebased.Grant.TTLSeconds != 10 {
		t.Fatalf("expected live rebased TTL=10s, got live=%v delta=%+v", live, rebased)
	}

	// Phase 2: Advance 11 seconds past deadline -> rebased reports !live, eachChatGrant yields 0 and prunes row.
	tAfter := clk.Advance(11 * time.Second)
	if _, stillLive := seenBefore[0].rebased(tAfter); stillLive {
		t.Fatalf("expected chat grant to expire after deadline")
	}
	var seenAfter []ChatGrant
	if err := store.eachChatGrant(ctx, nil, tAfter, func(g ChatGrant) error {
		seenAfter = append(seenAfter, g)
		return nil
	}); err != nil {
		t.Fatalf("eachChatGrant after expiry: %v", err)
	}
	if len(seenAfter) != 0 {
		t.Fatalf("expected 0 live chat grants after expiry, got %d", len(seenAfter))
	}

	// Independent SQLite check: row was pruned by DeleteExpiredChatGrants.
	var remaining int
	if err := db.New(sqlDB).ScanChatGrants(ctx, func(db.ListChatGrantsRow) error {
		remaining++
		return nil
	}); err != nil {
		t.Fatalf("ScanChatGrants: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("expected expired chat grant row to be pruned, remaining=%d", remaining)
	}
}
