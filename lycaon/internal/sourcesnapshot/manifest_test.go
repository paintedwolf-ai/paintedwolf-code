package sourcesnapshot

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// manyFiles spans several chunks at the default cut density.
const manyFiles = 5000

func snapshotChunks(t *testing.T, store *Store, snapshotID string) map[string]bool {
	t.Helper()
	rows, err := store.db.QueryContext(t.Context(), `
		SELECT l.chunk_id FROM source_snapshot_roots r
		JOIN source_manifest_layout l ON l.manifest_id = r.manifest_id
		WHERE r.snapshot_id = ?`, snapshotID)
	testutil.FailErr(t, "list snapshot chunks", err)
	defer func() { _ = rows.Close() }()
	out := make(map[string]bool)
	for rows.Next() {
		var id string
		testutil.FailErr(t, "scan chunk id", rows.Scan(&id))
		out[id] = true
	}
	testutil.FailErr(t, "read chunk ids", rows.Err())
	return out
}

func syntheticEntry(path string, version int) Entry {
	return Entry{
		Path: path, SHA256: fmt.Sprintf("%064x", version+len(path)), Identity: IdentityHashed,
		Size: int64(len(path) + version), Mode: 0o644,
	}
}

func buildEntries(t *testing.T, store *Store, entries []Entry) (rootManifest, []manifestChunk) {
	t.Helper()
	held := store.pins.holdManifests()
	t.Cleanup(held.release)
	builder := store.newManifestBuilder(held)
	for _, entry := range entries {
		testutil.FailErr(t, "add entry", builder.add(t.Context(), entry))
	}
	manifest, err := builder.finish(t.Context())
	testutil.FailErr(t, "finish manifest", err)
	layout, err := store.manifestLayout(t.Context(), manifest.id)
	testutil.FailErr(t, "read layout", err)
	return manifest, layout
}

// Rebuilding from a base must yield exactly what a full build of the edited
// entries yields, or one tree would answer to two identities.
func TestIncrementalBuildMatchesFullBuild(t *testing.T) {
	store := openSnapshotStore(t)
	random := rand.New(rand.NewPCG(7, 11))
	paths := make([]string, 0, 20000)
	for i := range 20000 {
		paths = append(paths, fmt.Sprintf("dir-%02d/file-%05d.go", i%37, i))
	}
	current := make(map[string]Entry, len(paths))
	for _, path := range paths {
		current[path] = syntheticEntry(path, 0)
	}
	ordered := func() []Entry {
		out := make([]Entry, 0, len(current))
		for _, path := range sortedKeys(current) {
			out = append(out, current[path])
		}
		return out
	}
	_, layout := buildEntries(t, store, ordered())
	if len(layout) < 8 {
		t.Fatalf("base built %d chunks, want several", len(layout))
	}
	for round := range 12 {
		overrides := make(map[string]*Entry)
		for range 1 + random.IntN(40) {
			switch path := paths[random.IntN(len(paths))]; random.IntN(3) {
			case 0:
				delete(current, path)
				overrides[path] = nil
			case 1:
				changed := syntheticEntry(path, round+1)
				current[path] = changed
				overrides[path] = &changed
			default:
				added := syntheticEntry(fmt.Sprintf("%s.r%d", path, round), round)
				current[added.Path] = added
				overrides[added.Path] = &added
			}
		}
		held := store.pins.holdManifests()
		incremental, err := store.newManifestBuilder(held).buildFromBase(t.Context(), layout, overrides)
		testutil.FailErr(t, "rebuild from base", err)
		full, fullLayout := buildEntries(t, store, ordered())
		held.release()
		if incremental.id != full.id || incremental.entryCount != int64(len(current)) {
			t.Fatalf("round %d: incremental %s (%d entries) != full %s (%d entries)",
				round, incremental.id, incremental.entryCount, full.id, len(current))
		}
		layout = fullLayout
	}
}

func sortedKeys(entries map[string]Entry) []string {
	keys := make([]string, 0, len(entries))
	for path := range entries {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	return keys
}

// One root captured alone and as part of a set is one manifest.
func TestRootManifestIsSharedAcrossRootSets(t *testing.T) {
	store := openSnapshotStore(t)
	shared, other := t.TempDir(), t.TempDir()
	for i := range 300 {
		writeSource(t, shared, fmt.Sprintf("pkg/file-%03d.go", i), fmt.Sprintf("package pkg // %d\n", i))
	}
	writeSource(t, other, "main.go", "package main\n")
	alone, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: shared}}, Verify: VerifyContent})
	testutil.FailErr(t, "publish single root", err)
	set, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: shared}, {Path: other}}, Verify: VerifyContent})
	testutil.FailErr(t, "publish root set", err)
	if alone.ID == set.ID {
		t.Fatal("a root set reused a single-root snapshot")
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM source_manifests`); got != 2 {
		t.Fatalf("manifests = %d, want one per distinct root", got)
	}
	var sharedRoots int
	testutil.FailErr(t, "count shared manifest", store.db.QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM source_snapshot_roots a JOIN source_snapshot_roots b ON a.manifest_id = b.manifest_id
		WHERE a.snapshot_id = ? AND b.snapshot_id = ? AND a.root_path = ?`, alone.ID, set.ID, shared).Scan(&sharedRoots))
	if sharedRoots != 1 {
		t.Fatalf("root set did not share the single-root manifest")
	}
	entry, ok, err := store.Lookup(t.Context(), set.ID, shared, "pkg/file-042.go")
	testutil.FailErr(t, "look up shared entry", err)
	if !ok || entry.RootPath != shared {
		t.Fatalf("lookup through a shared manifest = %+v, %v", entry, ok)
	}
}

// A chunk written ahead of its manifest, and a manifest a reader holds,
// survive a sweep until released.
func TestSweepSparesPinnedChunksAndManifests(t *testing.T) {
	store := openSnapshotStore(t)
	held := store.pins.holdManifests()
	builder := store.newManifestBuilder(held)
	for i := range 3 * chunkMinEntries {
		testutil.FailErr(t, "add entry", builder.add(t.Context(), syntheticEntry(fmt.Sprintf("f-%04d", i), 0)))
	}
	testutil.FailErr(t, "cut chunk", builder.cut(t.Context()))
	manifest, err := builder.finish(t.Context())
	testutil.FailErr(t, "finish manifest", err)
	testutil.FailErr(t, "sweep while pinned", store.Sweep(t.Context(), time.Now()))
	if countRows(t, store, `SELECT COUNT(*) FROM source_manifest_chunks`) == 0 ||
		countRows(t, store, fmt.Sprintf(`SELECT COUNT(*) FROM source_manifests WHERE id = '%s'`, manifest.id)) != 1 {
		t.Fatal("sweep removed pinned rows")
	}
	held.release()
	testutil.FailErr(t, "sweep after release", store.Sweep(t.Context(), time.Now()))
	for _, table := range []string{"source_manifests", "source_manifest_layout", "source_manifest_chunks", "source_manifest_entries"} {
		if got := countRows(t, store, "SELECT COUNT(*) FROM "+table); got != 0 {
			t.Fatalf("%s after unpinned sweep = %d, want 0", table, got)
		}
	}
}

// One sweep reclaims every unreferenced chunk, however many transactions
// that takes.
func TestSweepRunsUntilNothingIsUnreferenced(t *testing.T) {
	store := openSnapshotStore(t)
	held := store.pins.holdManifests()
	builder := store.newManifestBuilder(held)
	total := 3 * chunkSweepEntries
	for i := range total {
		testutil.FailErr(t, "add entry", builder.add(t.Context(), syntheticEntry(fmt.Sprintf("p-%06d", i), 0)))
	}
	testutil.FailErr(t, "cut chunk", builder.cut(t.Context()))
	held.release()
	if got := countRows(t, store, `SELECT COUNT(*) FROM source_manifest_entries`); got != total {
		t.Fatalf("entries before sweep = %d, want %d", got, total)
	}
	testutil.FailErr(t, "sweep", store.Sweep(t.Context(), time.Now()))
	if got := countRows(t, store, `SELECT COUNT(*) FROM source_manifest_entries`); got != 0 {
		t.Fatalf("entries after one sweep = %d, want 0", got)
	}
}

// Every read of a generation is answered from the snapshot's own manifest
// and layout, never by scanning entries across manifests.
func TestManifestReadsStayWithinTheirManifest(t *testing.T) {
	store := openSnapshotStore(t)
	for name, query := range map[string]string{
		"entry":       `SELECT e.path FROM source_manifest_entries e WHERE e.chunk_id = (SELECT l.chunk_id FROM source_manifest_layout l WHERE l.manifest_id = 'm' AND l.first_path <= 'p' ORDER BY l.first_path DESC LIMIT 1) AND e.path = 'p'`,
		"layout":      `SELECT l.first_path FROM source_manifest_layout l WHERE l.manifest_id = 'm' AND l.first_path >= COALESCE((SELECT s.first_path FROM source_manifest_layout s WHERE s.manifest_id = 'm' AND s.first_path <= 'a/' ORDER BY s.first_path DESC LIMIT 1), '') AND l.first_path < 'a0'`,
		"chunk range": `SELECT path FROM source_manifest_entries WHERE chunk_id = 'c' AND path > 'a/' AND path < 'a0' ORDER BY path`,
		"hashed":      `SELECT e.path FROM source_manifest_layout l CROSS JOIN source_manifest_chunks c ON c.id = l.chunk_id CROSS JOIN source_manifest_entries e ON e.chunk_id = l.chunk_id WHERE l.manifest_id = 'm' AND c.hashed_count > 0 AND e.identity = 'hashed' AND e.size <= 10 ORDER BY l.first_path, e.path LIMIT 5`,
		"root":        `SELECT e.path FROM source_snapshot_roots r CROSS JOIN source_manifest_layout l ON l.manifest_id = r.manifest_id CROSS JOIN source_manifest_entries e ON e.chunk_id = l.chunk_id WHERE r.snapshot_id = 's' AND r.root_path = 'r' ORDER BY l.first_path, e.path`,
	} {
		rows, err := store.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query)
		testutil.FailErr(t, "explain "+name, err)
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			testutil.FailErr(t, "scan plan", rows.Scan(&id, &parent, &unused, &detail))
			plan = append(plan, detail)
		}
		testutil.FailErr(t, "read plan", rows.Err())
		_ = rows.Close()
		joined := strings.Join(plan, "; ")
		if strings.Contains(joined, "SCAN") || strings.Contains(joined, "TEMP B-TREE") {
			t.Errorf("%s plan scans or sorts: %s", name, joined)
		}
	}
}

func TestSharedContentKeepsEachRootsRetentionEvidence(t *testing.T) {
	store := openSnapshotStore(t)
	indexed := Entry{Path: "same.go", GitOID: "shared-git-object", Identity: IdentityIndex, Size: 7, Mode: 0o644, ModifiedNS: 1}
	hashed := indexed
	hashed.SHA256, hashed.Identity, hashed.ModifiedNS = "retained-content-digest", IdentityHashed, 2
	first, _ := buildEntries(t, store, []Entry{indexed})
	second, _ := buildEntries(t, store, []Entry{hashed})
	if first.id == second.id {
		t.Fatal("distinct retention evidence reused stored rows")
	}
	if first.contentID != second.contentID {
		t.Fatal("identical bytes have different content identities")
	}
	got, ok, err := store.manifestLookup(t.Context(), second.id, "/other/root", hashed.Path)
	testutil.FailErr(t, "read retained entry", err)
	if !ok || got.SHA256 != hashed.SHA256 || got.Identity != IdentityHashed || got.ModifiedNS != hashed.ModifiedNS {
		t.Fatalf("shared content lost retention evidence: %+v", got)
	}
}
