package webindex

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func docOrigin(t *testing.T, s *Store, url string) string {
	t.Helper()
	var origin string
	testutil.FailErr(t, "read origin", s.db.QueryRow(`SELECT origin FROM docs WHERE url = ?`, url).Scan(&origin))
	return origin
}

func TestOriginEarnedDominatesUpsert(t *testing.T) {
	s := openTest(t)
	url := "https://a.example/doc"
	s.QueuePage(t.Context(), Page{URL: url, Title: "widget docs", Origin: OriginWarmed})
	s.Flush()
	if got := docOrigin(t, s, url); got != OriginWarmed {
		t.Fatalf("origin = %q want warmed", got)
	}
	// Verification promotes the page to earned.
	s.QueuePage(t.Context(), Page{URL: url, Title: "widget docs", Verified: true})
	s.Flush()
	if got := docOrigin(t, s, url); got != OriginEarned {
		t.Fatalf("origin = %q want earned after real search", got)
	}
	// Warm observations preserve earned status.
	s.QueuePage(t.Context(), Page{URL: url, Title: "widget docs", Origin: OriginWarmed})
	s.Flush()
	if got := docOrigin(t, s, url); got != OriginEarned {
		t.Fatalf("origin = %q want earned retained over warm re-observation", got)
	}
}

func TestOriginEarnedDominatesAnchorObservation(t *testing.T) {
	s := openTest(t)
	url := "https://example.com/anchor-only"
	s.QueueAnchors(t.Context(), url, []string{"background discovery"}, OriginWarmed)
	s.Flush()
	if got := docOrigin(t, s, url); got != OriginWarmed {
		t.Fatalf("origin = %q want warmed", got)
	}

	s.QueueAnchors(t.Context(), url, []string{"live discovery"}, OriginEarned)
	s.Flush()
	if got := docOrigin(t, s, url); got != OriginEarned {
		t.Fatalf("origin = %q want earned after live anchor", got)
	}

	s.QueueAnchors(t.Context(), url, []string{"background rediscovery"}, OriginWarmed)
	s.Flush()
	if got := docOrigin(t, s, url); got != OriginEarned {
		t.Fatalf("origin = %q want earned retained", got)
	}
}

func TestWarmHitConversionCounter(t *testing.T) {
	s := openTest(t)
	url := "https://a.example/warm-then-hit"
	s.QueuePage(t.Context(), Page{URL: url, Title: "widget frobnicator docs", Origin: OriginWarmed})
	// Search verification records one conversion.
	s.QueuePage(t.Context(), Page{URL: url, Title: "widget frobnicator docs", Verified: true})
	// Repeated verification keeps the conversion count unchanged.
	s.QueuePage(t.Context(), Page{URL: url, Title: "widget frobnicator docs", Verified: true})
	s.Flush()
	st, err := s.Stats(context.Background())
	testutil.FailErr(t, "stats", err)
	if st.WarmHits != 1 {
		t.Fatalf("warm_hits = %d want 1", st.WarmHits)
	}
	if st.Docs != 1 || st.Verified != 1 || st.Warmed != 0 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestEvictionPrefersWarmedOverEarned(t *testing.T) {
	s := openTest(t)
	// Unverified pages are evicted before older earned pages.
	for i := 0; i < 40; i++ {
		s.QueuePage(t.Context(), Page{URL: fmt.Sprintf("https://earned.example/p%d", i), Title: "earned widget page content"})
	}
	s.Flush()
	_, err := s.db.Exec(`UPDATE docs SET updated_at = 1 WHERE origin = 'earned'`)
	testutil.FailErr(t, "age earned fixtures", err)
	for i := 0; i < 40; i++ {
		s.QueuePage(t.Context(), Page{URL: fmt.Sprintf("https://warmed.example/p%d", i), Title: "warmed widget page content", Origin: OriginWarmed})
	}
	s.Flush()
	_, err = s.db.Exec(`UPDATE docs SET updated_at = 2 WHERE origin = 'warmed'`)
	testutil.FailErr(t, "freshen warmed fixtures", err)
	// This eviction chunk fits all 40 unverified pages.
	dropped, err := evictDropChunk(t.Context(), s.db, 40)
	testutil.FailErr(t, "evict drop chunk", err)
	if dropped != 40 {
		t.Fatalf("dropped = %d want 40", dropped)
	}

	var earned, warmed int
	testutil.FailErr(t, "count earned", s.db.QueryRow(`SELECT COUNT(*) FROM docs WHERE origin = 'earned'`).Scan(&earned))
	testutil.FailErr(t, "count warmed", s.db.QueryRow(`SELECT COUNT(*) FROM docs WHERE origin = 'warmed'`).Scan(&warmed))
	if warmed != 0 || earned != 40 {
		t.Fatalf("earned = %d warmed = %d: warmed must evict first, earned untouched", earned, warmed)
	}
}

func TestActivityLogRoundTripAndCap(t *testing.T) {
	s := openTest(t)
	for i := 0; i < MaxActivityRows+25; i++ {
		s.QueueActivity(t.Context(), WarmActivity{
			At:      time.Now().Add(time.Duration(i) * time.Second),
			Trigger: "turn", Tier: "crawl", Topic: fmt.Sprintf("topic %d", i),
			Hosts: []string{"a.example", "b.example"}, Pages: i,
		})
	}
	s.Flush()
	recent, err := s.RecentActivity(context.Background(), 10)
	testutil.FailErr(t, "recent", err)
	if len(recent) != 10 || recent[0].Pages < recent[9].Pages {
		t.Fatalf("recent = %+v want newest first", recent)
	}
	if len(recent[0].Hosts) != 2 {
		t.Fatalf("hosts = %v", recent[0].Hosts)
	}
	var rows int
	testutil.FailErr(t, "count", s.db.QueryRow(`SELECT COUNT(*) FROM warm_activity`).Scan(&rows))
	if rows > MaxActivityRows {
		t.Fatalf("activity rows = %d want capped at %d", rows, MaxActivityRows)
	}
}

func TestActivityAttributionRoundTrip(t *testing.T) {
	s := openTest(t)
	s.QueueActivity(t.Context(), WarmActivity{
		Trigger: "search", Tier: "seed", Topic: "widget docs",
		SessionID: "sess-1", ToolCallID: "call-9",
	})
	s.QueueActivity(t.Context(), WarmActivity{Trigger: "history_rewarm"}) // scheduler-initiated: no attribution
	s.Flush()
	recent, err := s.RecentActivity(context.Background(), 2)
	testutil.FailErr(t, "recent", err)
	if len(recent) != 2 {
		t.Fatalf("recent = %+v want both rows", recent)
	}
	byTrigger := map[string]WarmActivity{}
	for _, a := range recent {
		byTrigger[a.Trigger] = a
	}
	if a := byTrigger["search"]; a.SessionID != "sess-1" || a.ToolCallID != "call-9" {
		t.Fatalf("activity = %+v want attribution round-tripped", a)
	}
	if a := byTrigger["history_rewarm"]; a.SessionID != "" || a.ToolCallID != "" {
		t.Fatalf("activity = %+v want empty attribution for scheduler work", a)
	}
}

func TestReserveSeedWarmIsAtomicAndWindowed(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		ok, err := s.ReserveSeedWarm(ctx, now, time.Hour, 2)
		testutil.FailErr(t, "reserve seed warm", err)
		if !ok {
			t.Fatalf("reservation %d rejected", i+1)
		}
	}
	ok, err := s.ReserveSeedWarm(ctx, now, time.Hour, 2)
	testutil.FailErr(t, "reject capped seed warm", err)
	if ok {
		t.Fatal("third reservation admitted past cap")
	}
	ok, err = s.ReserveSeedWarm(ctx, now.Add(2*time.Hour), time.Hour, 2)
	testutil.FailErr(t, "reserve after window", err)
	if !ok {
		t.Fatal("reservation rejected after prior rows expired")
	}
}

func TestReserveSeedWarmConcurrentCallersShareOneCap(t *testing.T) {
	s := openTest(t)
	now := time.Now()
	type result struct {
		ok  bool
		err error
	}
	results := make(chan result, 8)
	var ready sync.WaitGroup
	ready.Add(8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			ready.Done()
			<-start
			ok, err := s.ReserveSeedWarm(context.Background(), now, time.Hour, 1)
			results <- result{ok: ok, err: err}
		}()
	}
	ready.Wait()
	close(start)
	admitted := 0
	for i := 0; i < 8; i++ {
		res := <-results
		testutil.FailErr(t, "reserve concurrent seed warm", res.err)
		if res.ok {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted = %d want exactly 1", admitted)
	}
}

func TestHostsForRewarmStalestVerifiedFirst(t *testing.T) {
	s := openTest(t)
	s.QueuePage(t.Context(), Page{URL: "https://stale.example/a", Title: "widget a", Verified: true})
	s.QueuePage(t.Context(), Page{URL: "https://fresh.example/b", Title: "widget b", Verified: true})
	s.QueuePage(t.Context(), Page{URL: "https://never.example/c", Title: "widget c"}) // unverified: excluded
	s.Flush()
	// Age the stale host's fetch stamp directly.
	_, err := s.db.Exec(`UPDATE docs SET fetched_at = ? WHERE host = 'stale.example'`, time.Now().Add(-48*time.Hour).Unix())
	testutil.FailErr(t, "age host", err)

	hosts, err := s.HostsForRewarm(context.Background(), 5)
	testutil.FailErr(t, "rewarm hosts", err)
	if len(hosts) != 2 || hosts[0] != "stale.example" {
		t.Fatalf("hosts = %v want stalest verified first, unverified excluded", hosts)
	}
}

func TestWarmStateRoundTrip(t *testing.T) {
	s := openTest(t)
	if v, err := s.GetWarmState(context.Background(), "missing"); err != nil || v != "" {
		t.Fatalf("missing key: v=%q err=%v", v, err)
	}
	s.SetWarmState(t.Context(), "manifest:/repo", "hash1")
	s.SetWarmState(t.Context(), "manifest:/repo", "hash2")
	s.Flush()
	v, err := s.GetWarmState(context.Background(), "manifest:/repo")
	testutil.FailErr(t, "get", err)
	if v != "hash2" {
		t.Fatalf("v = %q want latest value", v)
	}
}
