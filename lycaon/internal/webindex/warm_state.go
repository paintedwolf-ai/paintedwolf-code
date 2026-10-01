package webindex

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// IndexStats is the inspection snapshot surfaced in settings.
type IndexStats struct {
	Docs     int64
	Hosts    int64
	Bytes    int64
	Verified int64
	Warmed   int64
	// WarmHits counts warmed pages verified by search.
	WarmHits int64
	// Async writer counters.
	WritesApplied  int64
	WritesDropped  int64
	WritesFailed   int64
	QueueHighWater int64
	// Latest eviction pass.
	LastEvictMs    int64
	LastEvictRows  int64
	LastEvictBytes int64
	// Latest and maximum search latency.
	LastSearchMs int64
	MaxSearchMs  int64
}

// WarmActivity is one background-warming action (or recorded skip).
type WarmActivity struct {
	At         time.Time
	Trigger    string
	Tier       string // "crawl" | "seed" | ""
	Topic      string
	Hosts      []string
	Pages      int
	DurationMs int64
	SkipReason string
	// Empty attribution marks scheduled work.
	SessionID  string
	ToolCallID string
}

// Stats reads the current index snapshot.
func (s *Store) Stats(ctx context.Context) (IndexStats, error) {
	if s == nil {
		return IndexStats{}, nil
	}
	var st IndexStats
	row := s.readDB.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COUNT(DISTINCT host),
		COALESCE(SUM(verified), 0),
		COALESCE(SUM(origin = 'warmed'), 0)
		FROM docs`)
	if err := row.Scan(&st.Docs, &st.Hosts, &st.Verified, &st.Warmed); err != nil {
		return IndexStats{}, err
	}
	_ = s.readDB.QueryRowContext(ctx, `SELECT value FROM stats WHERE key = 'warm_hits'`).Scan(&st.WarmHits)
	bytes, err := fileBytes(ctx, s.readDB)
	if err != nil {
		return IndexStats{}, err
	}
	st.Bytes = bytes
	st.WritesApplied = s.writesApplied.Load()
	st.WritesDropped = s.writesDropped.Load()
	st.WritesFailed = s.writesFailed.Load()
	st.QueueHighWater = s.queueHighWater.Load()
	st.LastEvictMs = s.lastEvictMs.Load()
	st.LastEvictRows = s.lastEvictRows.Load()
	st.LastEvictBytes = s.lastEvictBytes.Load()
	st.LastSearchMs = s.lastSearchMs.Load()
	st.MaxSearchMs = s.maxSearchMs.Load()
	return st, nil
}

// QueueActivity appends to the bounded activity log.
func (s *Store) QueueActivity(ctx context.Context, a WarmActivity) {
	if s == nil {
		return
	}
	at := a.At
	if at.IsZero() {
		at = time.Now()
	}
	ctx = context.WithoutCancel(ctx)
	s.enqueue(func(db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `INSERT INTO warm_activity (at, trigger, tier, topic, hosts, pages, duration_ms, skip_reason, session_id, tool_call_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			at.Unix(), a.Trigger, a.Tier, strings.TrimSpace(a.Topic),
			strings.Join(a.Hosts, " "), a.Pages, a.DurationMs, a.SkipReason,
			a.SessionID, a.ToolCallID); err != nil {
			return fmt.Errorf("record warm activity: %w", err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM warm_activity WHERE id NOT IN (
			SELECT id FROM warm_activity ORDER BY at DESC, id DESC LIMIT ?)`, MaxActivityRows); err != nil {
			return fmt.Errorf("cap warm activity rows: %w", err)
		}
		return nil
	})
}

// RecentActivity returns the newest activity rows, newest first.
func (s *Store) RecentActivity(ctx context.Context, n int) ([]WarmActivity, error) {
	if s == nil || n <= 0 {
		return nil, nil
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT at, trigger, tier, topic, hosts, pages, duration_ms, skip_reason, session_id, tool_call_id
		FROM warm_activity ORDER BY at DESC, id DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []WarmActivity
	for rows.Next() {
		var a WarmActivity
		var at int64
		var hosts string
		if err := rows.Scan(&at, &a.Trigger, &a.Tier, &a.Topic, &hosts, &a.Pages, &a.DurationMs, &a.SkipReason, &a.SessionID, &a.ToolCallID); err != nil {
			return nil, err
		}
		a.At = time.Unix(at, 0)
		if hosts != "" {
			a.Hosts = strings.Fields(hosts)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ReserveSeedWarm atomically claims one model-seed slot in a rolling window.
func (s *Store) ReserveSeedWarm(ctx context.Context, at time.Time, window time.Duration, cap int) (bool, error) {
	if s == nil || cap <= 0 {
		return false, nil
	}
	cutoff := at.Add(-window).Unix()
	reserved := false
	err := s.syncWrite(ctx, func(db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `DELETE FROM seed_warm_reservations WHERE at < ?`, cutoff); err != nil {
			return err
		}
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM seed_warm_reservations WHERE at >= ?`, cutoff).Scan(&count); err != nil {
			return err
		}
		if count >= cap {
			return nil
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO seed_warm_reservations (at) VALUES (?)`, at.Unix()); err != nil {
			return err
		}
		reserved = true
		return nil
	})
	return reserved, err
}

// HostsForRewarm returns verified hosts, stalest first.
func (s *Store) HostsForRewarm(ctx context.Context, n int) ([]string, error) {
	if s == nil || n <= 0 {
		return nil, nil
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT host FROM docs
		WHERE verified = 1 AND host != ''
		GROUP BY host ORDER BY MIN(COALESCE(fetched_at, 0)) ASC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetWarmState reads a scheduler bookkeeping value ("" when absent).
func (s *Store) GetWarmState(ctx context.Context, key string) (string, error) {
	if s == nil {
		return "", nil
	}
	var v string
	err := s.readDB.QueryRowContext(ctx, `SELECT value FROM warm_state WHERE key = ?`, key).Scan(&v)
	if db.IsNoRows(err) {
		return "", nil
	}
	return v, err
}

// SetWarmState writes a scheduler bookkeeping value asynchronously.
func (s *Store) SetWarmState(ctx context.Context, key, value string) {
	if s == nil || strings.TrimSpace(key) == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	s.enqueue(func(db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `INSERT INTO warm_state (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return fmt.Errorf("write warm state: %w", err)
		}
		return nil
	})
}
