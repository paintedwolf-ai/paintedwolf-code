package webindex

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "web-index.db"))
	testutil.FailErr(t, "open store", err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestPageRoundTripAndRanking(t *testing.T) {
	s := openTest(t)
	s.QueuePage(t.Context(), Page{
		URL:         "https://www.example.com/reviews/steam-machine-review",
		Title:       "Steam Machine review: benchmarks and thermals",
		Description: "Valve's living room console tested.",
		Published:   time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		Verified:    true,
	})
	s.QueuePage(t.Context(), Page{
		URL:   "https://www.example.com/news/unrelated-gadget",
		Title: "A gadget appears",
	})
	s.Flush()

	docs, err := s.Search(context.Background(), "steam machine review", 10)
	testutil.FailErr(t, "search", err)
	if len(docs) != 1 {
		t.Fatalf("docs = %+v want the matching page only", docs)
	}
	d := docs[0]
	if d.Host != "www.example.com" || !d.Verified || d.Published.IsZero() {
		t.Fatalf("doc = %+v want host/verified/published preserved", d)
	}
}

func TestAnchorsMakeUnfetchedURLFindable(t *testing.T) {
	s := openTest(t)
	s.QueueAnchors(t.Context(), "https://newsite.example/steam-machine-verdict", []string{
		"The Steam Machine verdict after two weeks",
		"Steam Machine long-term review",
	}, OriginEarned)
	s.Flush()

	docs, err := s.Search(context.Background(), "steam machine verdict", 10)
	testutil.FailErr(t, "search", err)
	if len(docs) != 1 {
		t.Fatalf("docs = %+v want anchor-only URL findable", docs)
	}
	if docs[0].Title != "" || docs[0].Anchors == "" {
		t.Fatalf("doc = %+v want empty title, anchor aggregate present", docs[0])
	}
}

func TestPhraseAdjacencyOutranksScatteredTokens(t *testing.T) {
	s := openTest(t)
	s.QueuePage(t.Context(), Page{
		URL:   "https://a.example/phrase",
		Title: "steam machine review roundup",
	})
	s.QueuePage(t.Context(), Page{
		URL:   "https://b.example/scattered",
		Title: "steam cleaning machine — a review of a review",
	})
	s.Flush()
	docs, err := s.Search(context.Background(), "steam machine review", 10)
	testutil.FailErr(t, "search", err)
	if len(docs) != 2 || docs[0].URL != "https://a.example/phrase" {
		t.Fatalf("docs = %+v want phrase match first", docs)
	}
}

func TestUpsertKeepsBestFields(t *testing.T) {
	s := openTest(t)
	s.QueuePage(t.Context(), Page{URL: "https://a.example/p", Title: "Real title", Verified: true})
	// A sparse refresh preserves earned metadata.
	s.QueuePage(t.Context(), Page{URL: "https://a.example/p"})
	s.Flush()
	docs, err := s.Search(context.Background(), "real title", 10)
	testutil.FailErr(t, "search", err)
	if len(docs) != 1 || docs[0].Title != "Real title" || !docs[0].Verified {
		t.Fatalf("docs = %+v want title and verified retained", docs)
	}
}

func TestFTSQueryEscapesInput(t *testing.T) {
	if q := ftsQuery(`steam "machine) OR (evil`); q == "" {
		t.Fatal("want non-empty query")
	} else {
		s := openTest(t)
		if _, err := s.Search(context.Background(), `steam "machine) OR (evil`, 5); err != nil {
			t.Fatalf("hostile query must not produce FTS syntax error: %v", err)
		}
	}
}

func TestNilStoreIsInert(t *testing.T) {
	var s *Store
	s.QueuePage(t.Context(), Page{URL: "https://a.example"})
	s.QueueAnchors(t.Context(), "https://a.example", []string{"x"}, OriginEarned)
	s.Flush()
	if docs, err := s.Search(context.Background(), "x", 5); err != nil || docs != nil {
		t.Fatalf("nil store: docs=%v err=%v", docs, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("nil close: %v", err)
	}
}

func TestIncrementalVacuumEnabled(t *testing.T) {
	s := openTest(t)
	var mode int
	testutil.FailErr(t, "query auto_vacuum", s.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode))
	if mode != 2 {
		t.Fatalf("auto_vacuum = %d want 2 (INCREMENTAL) so evictions shrink the file", mode)
	}
}

func TestClearRemovesAllDocs(t *testing.T) {
	s := openTest(t)
	s.QueuePage(t.Context(), Page{URL: "https://a.example/p", Title: "steam machine"})
	s.QueueActivity(t.Context(), WarmActivity{Trigger: "turn", Pages: 2})
	s.Flush()
	testutil.FailErr(t, "clear", s.Clear(context.Background()))
	docs, err := s.Search(context.Background(), "steam machine", 10)
	testutil.FailErr(t, "search", err)
	if len(docs) != 0 {
		t.Fatalf("docs = %+v want empty index after clear", docs)
	}
	stats, err := s.Stats(context.Background())
	testutil.FailErr(t, "stats", err)
	if stats.Docs != 0 || stats.WarmHits != 0 {
		t.Fatalf("stats = %+v want zeroed counters", stats)
	}
	acts, err := s.RecentActivity(context.Background(), 5)
	testutil.FailErr(t, "activity", err)
	if len(acts) != 0 {
		t.Fatalf("activity = %+v want cleared warming log", acts)
	}
}

func TestQueueDeleteRemovesDoc(t *testing.T) {
	s := openTest(t)
	url := "https://a.example/dead-page"
	s.QueuePage(t.Context(), Page{URL: url, Title: "steam machine dead page"})
	s.QueueAnchors(t.Context(), url, []string{"steam machine dead page link"}, OriginEarned)
	s.QueueDelete(t.Context(), url)
	s.Flush()
	docs, err := s.Search(context.Background(), "steam machine dead", 5)
	testutil.FailErr(t, "search", err)
	if len(docs) != 0 {
		t.Fatalf("docs = %+v want deleted URL gone from index and FTS", docs)
	}
	var anchorRows int
	testutil.FailErr(t, "count anchors", s.db.QueryRow("SELECT COUNT(*) FROM anchors WHERE url = ?", url).Scan(&anchorRows))
	if anchorRows != 0 {
		t.Fatalf("anchors = %d want 0", anchorRows)
	}
}

func TestAnchorRowsCappedPerURL(t *testing.T) {
	s := openTest(t)
	url := "https://a.example/popular"
	for i := 0; i < maxAnchorRowsPerDoc+20; i++ {
		s.QueueAnchors(t.Context(), url, []string{fmt.Sprintf("distinct anchor text number %d", i)}, OriginEarned)
	}
	s.Flush()
	var rows int
	testutil.FailErr(t, "count anchors", s.db.QueryRow("SELECT COUNT(*) FROM anchors WHERE url = ?", url).Scan(&rows))
	if rows > maxAnchorRowsPerDoc {
		t.Fatalf("anchor rows = %d want <= %d", rows, maxAnchorRowsPerDoc)
	}
}

func TestEvictOverByteBudgetShrinksToBudget(t *testing.T) {
	s := openTest(t)
	big := strings.Repeat("steam machine coverage text ", 40)
	for i := 0; i < 400; i++ {
		s.QueuePage(t.Context(), Page{URL: fmt.Sprintf("https://a.example/p%d", i), Title: big})
	}
	s.Flush()
	before, err := fileBytes(t.Context(), s.db)
	testutil.FailErr(t, "file bytes before", err)
	if before < 64<<10 {
		t.Fatalf("test corpus too small to evict: %d bytes", before)
	}
	budget := before / 4
	testutil.FailErr(t, "evict over byte budget", s.evictOverByteBudget(t.Context(), s.db, budget))
	after, err := fileBytes(t.Context(), s.db)
	testutil.FailErr(t, "file bytes after", err)
	if after > budget {
		t.Fatalf("file = %d bytes want <= %d after byte eviction", after, budget)
	}
}

func TestDroppedWritesCountedAndHighWaterRecorded(t *testing.T) {
	s := openTest(t)
	started, release := make(chan struct{}, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	s.enqueue(func(*sql.DB) error { started <- struct{}{}; <-release; return nil })
	testutil.Receive(t, "writer parked", started)
	for i := 0; i < opQueueSize+8; i++ {
		s.enqueue(func(*sql.DB) error { return nil })
	}
	if dropped := s.writesDropped.Load(); dropped != 8 {
		t.Fatalf("writes dropped = %d want 8", dropped)
	}
	if hw := s.queueHighWater.Load(); hw != opQueueSize {
		t.Fatalf("queue high water = %d want %d", hw, opQueueSize)
	}
	unblock()
	ctx, cancel := context.WithTimeout(t.Context(), testutil.Timeout(5*time.Second))
	defer cancel()
	// A required barrier cannot be dropped by the saturated lossy queue.
	testutil.FailErr(t, "drain queued writes", s.syncWrite(ctx, func(*sql.DB) error { return nil }))
	st, err := s.Stats(ctx)
	testutil.FailErr(t, "stats", err)
	if st.WritesDropped != 8 || st.QueueHighWater != opQueueSize || st.WritesApplied < opQueueSize+1 {
		t.Fatalf("stats = %+v want writer counters carried", st)
	}
}

func TestEvictionPassRecordsLastEviction(t *testing.T) {
	s := openTest(t)
	for i := 0; i < 50; i++ {
		s.QueuePage(t.Context(), Page{URL: fmt.Sprintf("https://a.example/p%d", i), Title: "steam machine page"})
	}
	s.Flush()
	testutil.FailErr(t, "eviction pass", s.evictionPass(t.Context(), s.db, 10, maxIndexBytes))
	st, err := s.Stats(context.Background())
	testutil.FailErr(t, "stats", err)
	if st.LastEvictRows != 40 {
		t.Fatalf("last evict rows = %d want 40", st.LastEvictRows)
	}
	if st.LastEvictMs < 0 || st.LastEvictBytes < 0 {
		t.Fatalf("stats = %+v want non-negative eviction record", st)
	}
}

func TestSearchUsesReadPoolWhileWriterHoldsWriteTxn(t *testing.T) {
	s := openTest(t)
	s.QueuePage(t.Context(), Page{URL: "https://a.example/p", Title: "steam machine concurrency"})
	s.Flush()

	started := make(chan struct{})
	release := make(chan struct{})
	s.enqueue(func(db *sql.DB) error {
		// Search uses the read pool while the writer connection is occupied.
		tx, err := db.Begin()
		if err != nil {
			close(started)
			return err
		}
		_, _ = tx.Exec(`INSERT INTO warm_state (key, value) VALUES ('busy', '1')
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`)
		close(started)
		<-release
		return tx.Rollback()
	})
	<-started
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	docs, err := s.Search(ctx, "steam machine concurrency", 5)
	testutil.FailErr(t, "search while writer busy", err)
	if len(docs) != 1 {
		t.Fatalf("docs = %+v want hit served by the read pool", docs)
	}
}

func TestSearchTimingCarriedInStats(t *testing.T) {
	s := openTest(t)
	s.QueuePage(t.Context(), Page{URL: "https://a.example/p", Title: "steam machine timing"})
	s.Flush()
	_, err := s.Search(context.Background(), "steam machine timing", 5)
	testutil.FailErr(t, "search", err)
	st, err := s.Stats(context.Background())
	testutil.FailErr(t, "stats", err)
	if st.MaxSearchMs < st.LastSearchMs || st.LastSearchMs < 0 {
		t.Fatalf("stats = %+v want max >= last >= 0", st)
	}
	// Stats exposes the recorded atomic counters.
	s.lastSearchMs.Store(7)
	s.maxSearchMs.Store(9)
	st, err = s.Stats(context.Background())
	testutil.FailErr(t, "stats after store", err)
	if st.LastSearchMs != 7 || st.MaxSearchMs != 9 {
		t.Fatalf("stats = %+v want stored search timings carried", st)
	}
}

func TestDefaultPathUnderHome(t *testing.T) {
	p, err := DefaultPath()
	testutil.FailErr(t, "default path", err)
	dir, err := configdir.UserConfigDir()
	testutil.FailErr(t, "UserConfigDir", err)
	if filepath.Dir(p) != dir || filepath.Base(p) != "web-index.db" {
		t.Fatalf("path = %q, want under %q", p, dir)
	}
}
