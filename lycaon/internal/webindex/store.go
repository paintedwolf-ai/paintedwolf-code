// Package webindex stores rebuildable page and anchor search data.
// Callers verify indexed candidates before presenting them.
package webindex

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/observability"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

var logger = observability.LazyComponent("webindex")

const (
	// schemaVersion recreates the rebuildable cache on mismatch.
	schemaVersion = 1

	fileMode = 0o600

	// Row and byte limits use recency eviction.
	maxDocs       = 50_000
	maxIndexBytes = 128 << 20
	evictEvery    = 256
	// evictChunk bounds each byte-eviction pass.
	evictChunk = 512

	// Anchor limits retain the newest observations per URL.
	maxAnchorsPerDoc    = 12
	maxAnchorRowsPerDoc = 32

	// opQueueSize bounds the lossy writer backlog.
	opQueueSize = 512

	// MaxActivityRows caps the warming activity log by recency.
	MaxActivityRows = 200
)

// slowThreshold controls operation timing logs.
const slowThreshold = 100 * time.Millisecond

// Earned origin dominates warmed origin.
const (
	OriginEarned = "earned"
	OriginWarmed = "warmed"
)

func normalizeOrigin(origin string) string {
	if origin == OriginWarmed {
		return OriginWarmed
	}
	return OriginEarned
}

// Store uses one async writer and a read-only query pool.
type Store struct {
	db     *sql.DB // single-connection handle used by the writer goroutine
	readDB *sql.DB // read-only pool for all query paths
	ops    chan queuedWrite
	wg     sync.WaitGroup
	close  sync.Once
	writes int

	// Writer and search metrics are updated concurrently.
	writesApplied  atomic.Int64
	writesDropped  atomic.Int64
	writesFailed   atomic.Int64
	queueHighWater atomic.Int64
	dropWarned     atomic.Bool
	failWarned     atomic.Bool
	lastEvictMs    atomic.Int64
	lastEvictRows  atomic.Int64
	lastEvictBytes atomic.Int64
	lastSearchMs   atomic.Int64
	maxSearchMs    atomic.Int64
}

// Page is one fetched page's indexable metadata.
type Page struct {
	URL         string
	Host        string
	Title       string
	Description string
	Published   time.Time
	// Verified marks pages that passed relevance verification.
	Verified bool
	// Origin is OriginEarned (default) or OriginWarmed.
	Origin string
}

// Doc is one index search result.
type Doc struct {
	URL         string
	Host        string
	Title       string
	Description string
	Anchors     string
	Published   time.Time
	Verified    bool
	// Origin is OriginEarned or OriginWarmed — the row's provenance.
	Origin string
}

// DefaultPath returns the web index path alongside the app store (UserConfigDir).
func DefaultPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "web-index.db"), nil
}

// Open opens (or creates) the index, wiping it on schema version mismatch.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create index directory: %w", err)
	}
	db, err := openVersioned(ctx, path)
	if err != nil {
		return nil, err
	}
	readDB, err := openReadPool(path)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(path, fileMode)
	s := &Store{db: db, readDB: readDB, ops: make(chan queuedWrite, opQueueSize)}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for op := range s.ops {
			start := time.Now()
			// Report the first write failure from the rebuildable queue.
			if err := op(db); err != nil {
				s.writesFailed.Add(1)
				if s.failWarned.CompareAndSwap(false, true) {
					logger.Warn("web index write failed; search data is degraded until it succeeds again", "error", err)
				}
			}
			s.writesApplied.Add(1)
			if d := time.Since(start); d > slowThreshold {
				logger.Debug("slow web index writer op", "duration_ms", d.Milliseconds())
			}
		}
	}()
	return s, nil
}

// openReadPool isolates query traffic from writer maintenance.
// Active readers may produce a partial checkpoint.
func openReadPool(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(10000)", path))
	if err != nil {
		return nil, fmt.Errorf("open web index read pool: %w", err)
	}
	db.SetMaxOpenConns(2)
	return db, nil
}

func openVersioned(ctx context.Context, path string) (*sql.DB, error) {
	current, err := currentVersion(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !current {
		// Recreate an unreadable or mismatched cache.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			_ = os.Remove(path + suffix)
		}
	}
	return openFile(ctx, path)
}

// currentVersion accepts missing files for fresh creation.
func currentVersion(ctx context.Context, path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return true, nil
	}
	database, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return false, nil
	}
	defer func() { _ = database.Close() }()
	var version int
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return false, contextErr
		}
		return false, nil
	}
	return version == schemaVersion, nil
}

func openFile(ctx context.Context, path string) (*sql.DB, error) {
	// Configure incremental vacuum before the file header is initialized.
	dsn := fmt.Sprintf("file:%s?_pragma=auto_vacuum(2)&_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open web index: %w", err)
	}
	// The writer uses this single connection.
	db.SetMaxOpenConns(1)
	schema, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("stamp schema version: %w", err)
	}
	return db, nil
}

// Close drains pending writes and closes both database handles.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.close.Do(func() {
		close(s.ops)
	})
	s.wg.Wait()
	return errors.Join(s.db.Close(), s.readDB.Close())
}

// queuedWrite reports its cache-write error to the writer goroutine.
type queuedWrite func(*sql.DB) error

// enqueue drops cache writes when the backlog is full.
func (s *Store) enqueue(op queuedWrite) {
	if s == nil {
		return
	}
	defer func() {
		// Closing the queue may race with enqueue.
		_ = recover()
	}()
	select {
	case s.ops <- op:
		raiseMax(&s.queueHighWater, int64(len(s.ops)))
	default:
		s.writesDropped.Add(1)
		// Report queue loss once per process.
		if s.dropWarned.CompareAndSwap(false, true) {
			logger.Warn("web index write queue full; overflow writes are being dropped", "queue_size", opQueueSize)
		}
	}
}

// raiseMax records a new maximum atomically.
func raiseMax(a *atomic.Int64, n int64) {
	for {
		cur := a.Load()
		if n <= cur || a.CompareAndSwap(cur, n) {
			return
		}
	}
}

// Flush blocks until every write queued before the call has been applied.
func (s *Store) Flush() {
	if s == nil {
		return
	}
	done := make(chan struct{})
	s.enqueue(func(*sql.DB) error { close(done); return nil })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// syncWrite waits for a required writer operation.
func (s *Store) syncWrite(ctx context.Context, op func(*sql.DB) error) error {
	if s == nil {
		return nil
	}
	done := make(chan error, 1)
	select {
	case s.ops <- func(db *sql.DB) error { done <- op(db); return nil }:
		raiseMax(&s.queueHighWater, int64(len(s.ops)))
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// QueuePage records a fetched page's metadata asynchronously.
func (s *Store) QueuePage(ctx context.Context, p Page) {
	p.URL = strings.TrimSpace(p.URL)
	if p.URL == "" {
		return
	}
	if p.Host == "" {
		p.Host = hostOf(p.URL)
	}
	p.Title = NormalizeWebTextForStorage(p.Title, 0)
	p.Description = NormalizeWebTextForStorage(p.Description, 0)
	now := time.Now().Unix()
	origin := normalizeOrigin(p.Origin)
	ctx = context.WithoutCancel(ctx)
	s.enqueue(func(db *sql.DB) error {
		var published any
		if !p.Published.IsZero() {
			published = p.Published.Unix()
		}
		// Count warmed pages verified by an earned search.
		if origin == OriginEarned && p.Verified {
			var prior string
			if err := db.QueryRowContext(ctx, `SELECT origin FROM docs WHERE url = ?`, p.URL).Scan(&prior); err == nil && prior == OriginWarmed {
				if _, err := db.ExecContext(ctx, `INSERT INTO stats (key, value) VALUES ('warm_hits', 1)
					ON CONFLICT(key) DO UPDATE SET value = value + 1`); err != nil {
					return fmt.Errorf("record warm hit: %w", err)
				}
			}
		}
		_, err := db.ExecContext(ctx, `
			INSERT INTO docs (url, host, title, description, published_at, fetched_at, verified, origin, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(url) DO UPDATE SET
				host = excluded.host,
				title = CASE WHEN excluded.title != '' THEN excluded.title ELSE docs.title END,
				description = CASE WHEN excluded.description != '' THEN excluded.description ELSE docs.description END,
				published_at = COALESCE(excluded.published_at, docs.published_at),
				fetched_at = excluded.fetched_at,
				verified = MAX(docs.verified, excluded.verified),
				origin = CASE WHEN docs.origin = 'earned' OR excluded.origin = 'earned' THEN 'earned' ELSE 'warmed' END,
				updated_at = excluded.updated_at`,
			p.URL, p.Host, strings.TrimSpace(p.Title), strings.TrimSpace(p.Description),
			published, now, boolInt(p.Verified), origin, now)
		if err != nil {
			return fmt.Errorf("upsert web index doc: %w", err)
		}
		if err := s.refreshFTS(ctx, db, p.URL); err != nil {
			return err
		}
		s.maybeEvict(ctx, db)
		return nil
	})
}

// QueueAnchors records searchable anchor text for a URL.
func (s *Store) QueueAnchors(ctx context.Context, target string, texts []string, origin string) {
	target = strings.TrimSpace(target)
	if target == "" || len(texts) == 0 {
		return
	}
	host := hostOf(target)
	now := time.Now().Unix()
	org := normalizeOrigin(origin)
	ctx = context.WithoutCancel(ctx)
	s.enqueue(func(db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO docs (url, host, origin, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(url) DO UPDATE SET
				origin = CASE WHEN docs.origin = 'earned' OR excluded.origin = 'earned' THEN 'earned' ELSE 'warmed' END,
				updated_at = excluded.updated_at`,
			target, host, org, now); err != nil {
			return fmt.Errorf("upsert anchor host doc: %w", err)
		}
		for _, text := range texts {
			text = NormalizeWebTextForStorage(text, 0)
			if text == "" {
				continue
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO anchors (url, text, seen_at) VALUES (?, ?, ?)
				ON CONFLICT(url, text) DO UPDATE SET seen_at = excluded.seen_at`,
				target, text, now); err != nil {
				return fmt.Errorf("insert anchor: %w", err)
			}
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM anchors WHERE url = ? AND text NOT IN (
			SELECT text FROM anchors WHERE url = ? ORDER BY seen_at DESC LIMIT ?)`,
			target, target, maxAnchorRowsPerDoc); err != nil {
			return fmt.Errorf("cap anchor rows: %w", err)
		}
		if _, err := db.ExecContext(ctx, `
			UPDATE docs SET anchors = COALESCE((
				SELECT group_concat(text, ' — ') FROM (
					SELECT text FROM anchors WHERE url = ? ORDER BY seen_at DESC LIMIT ?
				)
			), '') WHERE url = ?`, target, maxAnchorsPerDoc, target); err != nil {
			return fmt.Errorf("project anchors onto doc: %w", err)
		}
		if err := s.refreshFTS(ctx, db, target); err != nil {
			return err
		}
		s.maybeEvict(ctx, db)
		return nil
	})
}

func (s *Store) refreshFTS(ctx context.Context, db *sql.DB, url string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM docs_fts WHERE url = ?`, url); err != nil {
		return fmt.Errorf("clear fts row: %w", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO docs_fts (url, title, description, anchors)
		SELECT url, title, description, anchors FROM docs WHERE url = ?`, url); err != nil {
		return fmt.Errorf("rebuild fts row: %w", err)
	}
	return nil
}

// QueueDelete removes a URL and its search data.
func (s *Store) QueueDelete(ctx context.Context, url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	s.enqueue(func(db *sql.DB) error {
		if _, err := db.ExecContext(ctx, `DELETE FROM docs_fts WHERE url = ?`, url); err != nil {
			return fmt.Errorf("delete fts row: %w", err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM anchors WHERE url = ?`, url); err != nil {
			return fmt.Errorf("delete anchors: %w", err)
		}
		if _, err := db.ExecContext(ctx, `DELETE FROM docs WHERE url = ?`, url); err != nil {
			return fmt.Errorf("delete doc: %w", err)
		}
		return nil
	})
}

// Search returns ranked candidates for verification. Equal matches prefer the
// most recently fetched content; a URL known only from anchors has none.
func (s *Store) Search(ctx context.Context, query string, limit int) ([]Doc, error) {
	if s == nil {
		return nil, nil
	}
	match := ftsQuery(query)
	if match == "" || limit <= 0 {
		return nil, nil
	}
	start := time.Now()
	defer func() {
		ms := time.Since(start).Milliseconds()
		s.lastSearchMs.Store(ms)
		raiseMax(&s.maxSearchMs, ms)
		if ms > slowThreshold.Milliseconds() {
			logger.Debug("slow web index search", "duration_ms", ms)
		}
	}()
	rows, err := s.readDB.QueryContext(ctx, `
		SELECT d.url, d.host, d.title, d.description, d.anchors,
		       COALESCE(d.published_at, 0), d.verified, d.origin
		FROM docs_fts f JOIN docs d ON d.url = f.url
		WHERE docs_fts MATCH ?
		ORDER BY bm25(docs_fts), d.fetched_at DESC, d.url
		LIMIT ?`, match, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Doc
	for rows.Next() {
		var d Doc
		var published int64
		var verified int
		if err := rows.Scan(&d.URL, &d.Host, &d.Title, &d.Description, &d.Anchors, &published, &verified, &d.Origin); err != nil {
			return nil, err
		}
		if published > 0 {
			d.Published = time.Unix(published, 0)
		}
		d.Verified = verified != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// ftsQuery combines a quoted phrase with quoted individual tokens.
func ftsQuery(query string) string {
	fields := strings.Fields(strings.ToLower(query))
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, ".,;:!?\"'()[]{}")
		f = strings.ReplaceAll(f, `"`, "")
		if f != "" {
			tokens = append(tokens, f)
		}
	}
	if len(tokens) == 0 {
		return ""
	}
	parts := make([]string, 0, len(tokens)+1)
	if len(tokens) > 1 {
		parts = append(parts, `"`+strings.Join(tokens, " ")+`"`)
	}
	for _, tok := range tokens {
		parts = append(parts, `"`+tok+`"`)
	}
	return strings.Join(parts, " OR ")
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
