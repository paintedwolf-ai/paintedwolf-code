package sourcesnapshot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func seedChunk(t *testing.T, s *Store, id string, entries int) {
	t.Helper()
	ctx := t.Context()
	_, err := s.db.ExecContext(ctx, `INSERT INTO source_manifest_chunks (id,content_id,entry_count,total_bytes,hashed_count) VALUES (?,?,?,0,0)`, id, id, max(entries, 1))
	testutil.FailErr(t, "seed chunk", err)
	_, err = s.db.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<?)
 INSERT INTO source_manifest_entries (chunk_id,path,identity,size,mode) SELECT ?,printf('%08d',i),'stat',0,0 FROM n`, entries, id)
	testutil.FailErr(t, "seed chunk entries", err)
}

func seedManifest(t *testing.T, s *Store, id string, chunks ...string) {
	t.Helper()
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO source_manifests (id,entry_count,total_bytes) VALUES (?,1,0)`, id)
	testutil.FailErr(t, "seed manifest", err)
	for i, chunk := range chunks {
		_, err := s.db.ExecContext(t.Context(), `INSERT INTO source_manifest_layout (manifest_id,first_path,chunk_id) VALUES (?,?,?)`, id, fmt.Sprintf("%08d", i), chunk)
		testutil.FailErr(t, "seed layout", err)
	}
}

func seedSnapshot(t *testing.T, s *Store, id, created, manifest string) {
	t.Helper()
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO source_snapshots (id,roots_key,merkle_sha256,file_count,total_bytes,created_ts) VALUES (?,?,?,0,0,?)`, id, "roots-"+id, id, created)
	testutil.FailErr(t, "seed snapshot", err)
	_, err = s.db.ExecContext(t.Context(), `INSERT INTO source_snapshot_roots (snapshot_id,root_path,ordinal,manifest_id) VALUES (?,'/root',0,?)`, id, manifest)
	testutil.FailErr(t, "seed snapshot root", err)
}

func sweepAdmission(t *testing.T, s *Store) SweepReport {
	t.Helper()
	report, err := s.Sweep(t.Context(), time.Now().Add(-Retention))
	testutil.FailErr(t, "sweep admission", err)
	return report
}

func sweepCycle(t *testing.T, s *Store) (SweepReport, int) {
	t.Helper()
	var total SweepReport
	for admissions := 1; admissions < 10000; admissions++ {
		report := sweepAdmission(t, s)
		total.Examined += report.Examined
		total.Retained += report.Retained
		total.ChildRowsDeleted += report.ChildRowsDeleted
		total.ManifestsDeleted += report.ManifestsDeleted
		total.ChunksDeleted += report.ChunksDeleted
		total.SnapshotsDeleted += report.SnapshotsDeleted
		total.OversizedDeferred += report.OversizedDeferred
		total.MaxTxChildRows = max(total.MaxTxChildRows, report.MaxTxChildRows)
		if report.CycleComplete {
			return total, admissions
		}
	}
	t.Fatal("sweep cycle made no end")
	return total, 0
}

func TestSweepFollowsGraphAndKeepsRetainedReferences_AC_DB_01(t *testing.T) {
	s := openSnapshotStore(t)
	ctx := t.Context()
	old := db.FormatTime(time.Now().Add(-2 * Retention))
	seedChunk(t, s, "kept-chunk", 3)
	seedChunk(t, s, "gone-chunk", 3)
	seedManifest(t, s, "kept-manifest", "kept-chunk")
	seedManifest(t, s, "gone-manifest", "gone-chunk")
	seedSnapshot(t, s, "gone", old, "gone-manifest")
	_, err := s.db.ExecContext(ctx, `INSERT INTO source_snapshot_boundaries (snapshot_id,root_path,path,reason) VALUES ('gone','/root','x','scope')`)
	testutil.FailErr(t, "seed boundary", err)
	testdbseed.InsertProject(t, s.db, "project-inventory")
	references := []struct{ snapshot, query string }{
		{"by-head", `INSERT INTO source_snapshot_heads (roots_key,snapshot_id,published_ts) VALUES ('live','by-head','2999-01-01T00:00:00Z')`},
		{"by-inventory", `INSERT INTO source_inventory_state (project_id,requested_generation,completed_generation,phase,file_count,requested_ts,snapshot_id) VALUES ('project-inventory',1,1,'ready',1,'2999-01-01T00:00:00Z','by-inventory')`},
		{"by-scan", `INSERT INTO code_scans (id,canonical_path,categories_json,status,created_at,reuse_key,source_snapshot_id) VALUES ('scan','/p','[]','complete','2999-01-01T00:00:00Z','key','by-scan')`},
		{"by-series", `INSERT INTO scan_series (canonical_path,scanner_id,categories_json,last_covered_snapshot_id,updated_at) VALUES ('/p','s','[]','by-series','2999-01-01T00:00:00Z')`},
	}
	for _, reference := range references {
		seedSnapshot(t, s, reference.snapshot, old, "kept-manifest")
		_, err := s.db.ExecContext(ctx, reference.query)
		testutil.FailErr(t, "seed reference "+reference.snapshot, err)
	}
	report, count := sweepCycle(t, s)
	if count != 1 {
		t.Fatalf("small graph took %d admissions", count)
	}
	if report.SnapshotsDeleted != 1 || report.ManifestsDeleted != 1 || report.ChunksDeleted != 1 {
		t.Fatalf("deleted = %+v, want the one unreachable path", report)
	}
	// One root and one boundary, one layout row, three entries.
	if report.ChildRowsDeleted != 2+1+3 {
		t.Fatalf("cascaded rows = %d, want 6", report.ChildRowsDeleted)
	}
	if report.Retained != int64(len(references))+1+1 {
		t.Fatalf("retained = %d", report.Retained)
	}
	for _, check := range []struct {
		query string
		want  int
	}{
		{`SELECT COUNT(*) FROM source_snapshots`, len(references)},
		{`SELECT COUNT(*) FROM source_snapshot_roots`, len(references)},
		{`SELECT COUNT(*) FROM source_snapshot_boundaries`, 0},
		{`SELECT COUNT(*) FROM source_manifests WHERE id='kept-manifest'`, 1},
		{`SELECT COUNT(*) FROM source_manifest_layout`, 1},
		{`SELECT COUNT(*) FROM source_manifest_entries WHERE chunk_id='kept-chunk'`, 3},
		{`SELECT COUNT(*) FROM source_manifest_entries WHERE chunk_id='gone-chunk'`, 0},
	} {
		if got := countRows(t, s, check.query); got != check.want {
			t.Fatalf("%s = %d want %d", check.query, got, check.want)
		}
	}
}

func TestSweepKeepsSharedAndPinnedContentUntilFinalRelease_AC_DB_02(t *testing.T) {
	s := openSnapshotStore(t)
	old := db.FormatTime(time.Now().Add(-2 * Retention))
	seedChunk(t, s, "shared-chunk", 2)
	seedChunk(t, s, "built-chunk", 2)
	seedManifest(t, s, "shared", "shared-chunk")
	seedManifest(t, s, "building")
	seedSnapshot(t, s, "first", old, "shared")
	seedSnapshot(t, s, "second", old, "shared")
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO source_snapshot_heads (roots_key,snapshot_id,published_ts) VALUES ('held','second','2999-01-01T00:00:00Z')`)
	testutil.FailErr(t, "hold second root", err)
	builder := s.pins.holdManifests("building")
	builder.addChunk("built-chunk")

	sweepAdmission(t, s)
	if got := countRows(t, s, `SELECT COUNT(*) FROM source_snapshots`); got != 1 {
		t.Fatalf("snapshots = %d, want the held root only", got)
	}
	for _, kept := range []string{
		`SELECT COUNT(*) FROM source_manifests WHERE id='shared'`,
		`SELECT COUNT(*) FROM source_manifest_chunks WHERE id='shared-chunk'`,
		`SELECT COUNT(*) FROM source_manifests WHERE id='building'`,
		`SELECT COUNT(*) FROM source_manifest_chunks WHERE id='built-chunk'`,
	} {
		if countRows(t, s, kept) != 1 {
			t.Fatalf("collection removed content still in use: %s", kept)
		}
	}

	// A root published after the candidate read still wins at delete time.
	collector := snapshotCollector{db: s.db, queries: s.queries, pins: s.pins, cursor: &sweepCursor{}, limits: defaultSweepLimits}
	stale := sweepCandidate{id: "shared", childRows: 1}
	testutil.FailErr(t, "delete stale candidate", collector.removeCandidates(t.Context(), cascadeSweep{
		pinned: func(manifestPinned, _ func(string) bool) func(string) bool { return manifestPinned },
		remove: func(ctx context.Context, q *db.Queries, c sweepCandidate) (int64, error) {
			return q.DeleteUnreferencedSourceManifest(ctx, c.id)
		},
		deleted: &collector.report.ManifestsDeleted,
	}, []sweepCandidate{stale}))
	if collector.report.ManifestsDeleted != 0 || collector.report.Retained != 1 || collector.report.ChildRowsDeleted != 0 {
		t.Fatalf("stale candidate report = %+v", collector.report)
	}

	builder.release()
	_, err = s.db.ExecContext(t.Context(), `DELETE FROM source_snapshot_heads`)
	testutil.FailErr(t, "release held root", err)
	sweepAdmission(t, s)
	for _, table := range []string{"source_snapshots", "source_manifests", "source_manifest_chunks", "source_manifest_entries"} {
		if got := countRows(t, s, "SELECT COUNT(*) FROM "+table); got != 0 {
			t.Fatalf("%s after final release = %d", table, got)
		}
	}
}

func TestSweepCursorPassesRetainedCandidates_AC_DB_04(t *testing.T) {
	s := openSnapshotStore(t)
	s.sweepLimits = sweepLimits{candidates: manifestSweepBatch, rows: sweepTxChildRows}
	old := db.FormatTime(time.Now().Add(-2 * Retention))
	const retained = 3 * manifestSweepBatch
	for i := range retained {
		id := fmt.Sprintf("a-%04d", i)
		seedManifest(t, s, id)
		seedSnapshot(t, s, id, old, id)
		_, err := s.db.ExecContext(t.Context(), `INSERT INTO source_snapshot_heads (roots_key,snapshot_id,published_ts) VALUES (?,?,'2999-01-01T00:00:00Z')`, id, id)
		testutil.FailErr(t, "retain snapshot", err)
	}
	seedManifest(t, s, "z-garbage")

	admissions := 0
	var total SweepReport
	for {
		admissions++
		report := sweepAdmission(t, s)
		// A candidate page is read whole, so a limit can be passed by less than a page.
		if report.Examined >= s.sweepLimits.candidates+snapshotSweepBatch {
			t.Fatalf("admission %d examined %d", admissions, report.Examined)
		}
		total.Examined += report.Examined
		total.Transactions += report.Transactions
		total.ManifestsDeleted += report.ManifestsDeleted
		if report.CycleComplete {
			break
		}
	}
	if admissions < 2 {
		t.Fatal("the admission limit did not divide the cycle")
	}
	// Each candidate is read once per cycle: no admission restarted the scan.
	if want := int64(2*retained + 1); total.Examined != want {
		t.Fatalf("examined = %d, want %d", total.Examined, want)
	}
	if total.ManifestsDeleted != 1 {
		t.Fatalf("garbage behind the retained candidates was not reached: %+v", total)
	}
	// Only the garbage opened a writer: retained pages never did.
	if total.Transactions != 1 {
		t.Fatalf("transactions = %d, want 1", total.Transactions)
	}
}

func TestSweepBoundsCascadesPerTransactionAndAdmission_AC_DB_04(t *testing.T) {
	cases := []struct {
		name         string
		limits       sweepLimits
		seed         func(*testing.T, *Store)
		table        string
		wantRows     int64
		minAdmission int
	}{
		{
			name:   "chunk entries",
			limits: sweepLimits{candidates: 8192, rows: sweepTxChildRows},
			seed: func(t *testing.T, s *Store) {
				for i := range 10 {
					seedChunk(t, s, fmt.Sprintf("chunk-%d", i), 4096)
				}
			},
			table: "source_manifest_entries", wantRows: 40960, minAdmission: 3,
		},
		{
			name:   "manifest layout",
			limits: sweepLimits{candidates: 8192, rows: 2 * sweepTxChildRows},
			seed: func(t *testing.T, s *Store) {
				seedChunk(t, s, "shared", 1)
				for m := range 5 {
					_, err := s.db.ExecContext(t.Context(), `INSERT INTO source_manifests (id,entry_count,total_bytes) VALUES (?,1,0)`, fmt.Sprintf("m-%d", m))
					testutil.FailErr(t, "seed manifest", err)
					_, err = s.db.ExecContext(t.Context(), `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<12000)
 INSERT INTO source_manifest_layout (manifest_id,first_path,chunk_id) SELECT ?,printf('%08d',i),'shared' FROM n`, fmt.Sprintf("m-%d", m))
					testutil.FailErr(t, "seed layout fanout", err)
				}
			},
			table: "source_manifest_layout", wantRows: 60000 + 1, minAdmission: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openSnapshotStore(t)
			s.sweepLimits = tc.limits
			tc.seed(t, s)
			var total int64
			admissions := 0
			for {
				admissions++
				report := sweepAdmission(t, s)
				if report.MaxTxChildRows > sweepTxChildRows {
					t.Fatalf("transaction cascaded %d rows", report.MaxTxChildRows)
				}
				// The limit is checked between transactions, so one more may complete.
				if report.ChildRowsDeleted > tc.limits.rows+sweepTxChildRows {
					t.Fatalf("admission cascaded %d rows", report.ChildRowsDeleted)
				}
				total += report.ChildRowsDeleted
				if report.CycleComplete {
					break
				}
			}
			if admissions < tc.minAdmission {
				t.Fatalf("admissions = %d, want at least %d", admissions, tc.minAdmission)
			}
			if total != tc.wantRows {
				t.Fatalf("cascaded rows = %d want %d", total, tc.wantRows)
			}
			if got := countRows(t, s, "SELECT COUNT(*) FROM "+tc.table); got != 0 {
				t.Fatalf("%s left %d rows", tc.table, got)
			}
		})
	}
}

func TestSweepDefersOversizedObjectsVisibly_AC_DB_04(t *testing.T) {
	s := openSnapshotStore(t)
	seedChunk(t, s, "a-oversized", sweepTxChildRows+1)
	seedChunk(t, s, "b-garbage", 2)
	for cycle := range 2 {
		report, _ := sweepCycle(t, s)
		if report.OversizedDeferred != 1 {
			t.Fatalf("cycle %d oversized = %d", cycle, report.OversizedDeferred)
		}
		if report.MaxTxChildRows > sweepTxChildRows {
			t.Fatalf("cycle %d cascaded %d rows", cycle, report.MaxTxChildRows)
		}
		// The oversized object stays whole rather than partly deleted.
		if got := countRows(t, s, `SELECT COUNT(*) FROM source_manifest_entries WHERE chunk_id='a-oversized'`); got != sweepTxChildRows+1 {
			t.Fatalf("oversized entries = %d", got)
		}
	}
	if got := countRows(t, s, `SELECT COUNT(*) FROM source_manifest_chunks WHERE id='b-garbage'`); got != 0 {
		t.Fatal("an oversized object starved the garbage behind it")
	}
}

type failingSweepWriter struct {
	db.Handle
	remaining *int
	failure   error
}

func (d failingSweepWriter) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if *d.remaining == 0 {
		return nil, d.failure
	}
	*d.remaining--
	return d.Handle.BeginTx(ctx, opts)
}

func TestSweepResumesAfterCancellationAndRestart_AC_DB_04(t *testing.T) {
	s := openSnapshotStore(t)
	for i := range 6 {
		seedChunk(t, s, fmt.Sprintf("chunk-%d", i), 4096)
	}
	remaining := 1
	collector := snapshotCollector{
		db:      failingSweepWriter{Handle: s.db, remaining: &remaining, failure: context.Canceled},
		queries: s.queries, pins: s.pins, cursor: &s.sweepCursor, limits: defaultSweepLimits,
	}
	err := collector.sweep(t.Context(), db.FormatTime(time.Now().Add(-Retention)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cut off sweep = %v", err)
	}
	if collector.report.CycleComplete || collector.report.ChunksDeleted == 0 {
		t.Fatalf("report = %+v, want committed progress and an unfinished cycle", collector.report)
	}
	before := countRows(t, s, `SELECT COUNT(*) FROM source_manifest_entries`)
	if before == 0 || before%4096 != 0 {
		t.Fatalf("entries = %d, want whole chunks", before)
	}
	// A restart forgets the cursor and begins a new cycle.
	s.sweepCursor = sweepCursor{}
	report, _ := sweepCycle(t, s)
	if report.ChildRowsDeleted != int64(before) {
		t.Fatalf("resumed cascades = %d want %d", report.ChildRowsDeleted, before)
	}
}

func TestSweepReportsRetainedDataAsRetained_AC_DB_06(t *testing.T) {
	s := openSnapshotStore(t)
	old := db.FormatTime(time.Now().Add(-2 * Retention))
	seedChunk(t, s, "kept", 8)
	seedManifest(t, s, "kept", "kept")
	seedSnapshot(t, s, "kept", old, "kept")
	_, err := s.db.ExecContext(t.Context(), `INSERT INTO source_snapshot_heads (roots_key,snapshot_id,published_ts) VALUES ('held','kept','2999-01-01T00:00:00Z')`)
	testutil.FailErr(t, "hold snapshot", err)
	report := sweepAdmission(t, s)
	if !report.CycleComplete || report.Retained != 3 || report.Examined != 3 {
		t.Fatalf("report = %+v, want three retained candidates", report)
	}
	if report.SnapshotsDeleted+report.ManifestsDeleted+report.ChunksDeleted+report.ChildRowsDeleted != 0 {
		t.Fatalf("report = %+v, want nothing called deleted", report)
	}
}
