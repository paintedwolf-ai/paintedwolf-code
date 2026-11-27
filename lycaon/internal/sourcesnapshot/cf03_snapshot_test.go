package sourcesnapshot

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/timelayout"
)

const cf03SnapBaseSeed = "20261127"

type cf03SnapSummary struct {
	Family       string         `json:"family"`
	Lane         string         `json:"lane"`
	BaseSeed     string         `json:"base_seed"`
	Platform     string         `json:"platform"`
	Planned      int            `json:"planned"`
	Executed     int            `json:"executed"`
	Passed       int            `json:"passed"`
	Failed       int            `json:"failed"`
	DeclaredGaps map[string]int `json:"declared_gaps"`
	ChunkReuseOK int            `json:"chunk_reuse_ok"`
	GCSafeOK     int            `json:"gc_safe_ok"`
}

func cf03SnapSeedRNG(baseSeed, family string, instance int) *rand.Rand {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", baseSeed, family, instance)))
	return rand.New(rand.NewPCG(binary.BigEndian.Uint64(sum[:8]), binary.BigEndian.Uint64(sum[8:16])))
}

// TestCF03_K07_SnapshotsAndBoundaries runs 600 seeded in-process K07 instances (1-2,000 entries):
// 1) budget cuts, scope exclusions, and unreadable dirs recorded in source_snapshot_boundaries with exact reasons,
// 2) small edits count replaced vs reused manifest chunks,
// 3) Store.Sweep clean-up never removes any chunk referenced by a surviving manifest.
func TestCF03_K07_SnapshotsAndBoundaries(t *testing.T) {
	const planned = 600
	isRoot := os.Geteuid() == 0

	store := openSnapshotStore(t)
	gapCounts := map[string]int{}
	passed := 0
	chunkReuseOK := 0
	gcSafeOK := 0

	for idx := range planned {
		rng := cf03SnapSeedRNG(cf03SnapBaseSeed, "K07", idx)
		mode := idx % 3

		switch mode {
		case 0:
			// Snapshot boundaries: budget cuts + scope exclusions + unreadable dirs persisted in source_snapshot_boundaries.
			root := t.TempDir()
			writeSource(t, root, "src/main.go", fmt.Sprintf("package main // %d\n", idx))
			writeSource(t, root, ".gitignore", "ignored_dir/\n")
			writeSource(t, root, "ignored_dir/skip.txt", "ignored")
			writeSource(t, root, "node_modules/pkg/index.js", "floor")
			for i := range 10 {
				writeSource(t, root, fmt.Sprintf("wide/f_%02d.txt", i), "wide")
			}
			lockedDir := filepath.Join(root, "locked")
			if !isRoot {
				writeSource(t, root, "locked/secret.txt", "locked")
				testutil.FailErr(t, "chmod 000 locked", os.Chmod(lockedDir, 0o000))
			}

			scope := sourcescope.New(root, sourcescope.Options{
				Floor: []string{"node_modules", "node_modules/**"},
				Plane: sourcescope.Plane{
					IgnoreFiles: true,
					Budgets: sandbox.SurveyBudgets{
						DirectoryEntries: 7,
						SubtreeEntries:   100,
						WalkEntries:      1000,
					},
				},
			})
			snap, err := store.Ensure(t.Context(), Request{
				Roots:     []Root{{Path: root}},
				Verify:    VerifyStat,
				Scope:     scope,
				Unwatched: true,
			})
			if !isRoot {
				_ = os.Chmod(lockedDir, 0o755)
			}
			testutil.FailErr(t, "k07 capture boundaries", err)

			if snap.AdmissionMode != AdmissionScopeBounded {
				t.Fatalf("K07[%d] expected AdmissionScopeBounded, got %q", idx, snap.AdmissionMode)
			}
			// Verify persisted rows directly in source_snapshot_boundaries table.
			rows, err := store.db.QueryContext(t.Context(),
				`SELECT path, reason, detail FROM source_snapshot_boundaries WHERE snapshot_id = ?`, snap.ID)
			testutil.FailErr(t, "query source_snapshot_boundaries", err)
			byPath := map[string][2]string{}
			for rows.Next() {
				var p, reason, detail string
				testutil.FailErr(t, "scan boundary", rows.Scan(&p, &reason, &detail))
				byPath[p] = [2]string{reason, detail}
			}
			testutil.FailErr(t, "close boundary rows", rows.Close())

			if byPath["wide"][0] != string(sandbox.BoundaryDirectoryCap) {
				t.Fatalf("K07[%d] wide boundary=%v want directory_cap", idx, byPath["wide"])
			}
			if byPath["node_modules"][0] != "scope" || byPath["node_modules"][1] != sourcescope.ReasonFloor {
				t.Fatalf("K07[%d] node_modules boundary=%v want scope/floor", idx, byPath["node_modules"])
			}
			if byPath["ignored_dir"][0] != "scope" || byPath["ignored_dir"][1] != sourcescope.ReasonIgnored {
				t.Fatalf("K07[%d] ignored_dir boundary=%v want scope/ignored", idx, byPath["ignored_dir"])
			}
			if !isRoot {
				if _, ok := byPath["locked"]; !ok {
					t.Fatalf("K07[%d] unreadable dir 'locked' missing from source_snapshot_boundaries: %+v", idx, byPath)
				}
			}
			gapCounts["catalog_bounded"]++
			_ = os.RemoveAll(root)

		case 1:
			// Chunk reuse on small edits by count (using manifest builder over 200-2,000 entries).
			entryCount := 200 + rng.IntN(600)
			if idx == 1 {
				entryCount = 2000
			}
			entries := make([]Entry, entryCount)
			for i := range entryCount {
				p := fmt.Sprintf("dir_%02d/file_%05d.go", i%20, i)
				entries[i] = syntheticEntry(p, idx)
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
			_, baseLayout := buildEntries(t, store, entries)
			baseChunks := map[string]bool{}
			for _, c := range baseLayout {
				baseChunks[c.id] = true
			}
			// Mutate 1..3 paths and rebuild incrementally.
			edits := 1 + rng.IntN(3)
			overrides := map[string]*Entry{}
			for e := range edits {
				pos := rng.IntN(entryCount)
				updated := syntheticEntry(entries[pos].Path, idx+1000+e)
				overrides[updated.Path] = &updated
			}
			held := store.pins.holdManifests()
			nextManifest, err := store.newManifestBuilder(held).buildFromBase(t.Context(), baseLayout, overrides)
			testutil.FailErr(t, "k07 incremental build", err)
			nextLayout, err := store.manifestLayout(t.Context(), nextManifest.id)
			testutil.FailErr(t, "k07 next layout", err)
			held.release()

			reused := 0
			replaced := 0
			for _, c := range nextLayout {
				if baseChunks[c.id] {
					reused++
				} else {
					replaced++
				}
			}
			if len(baseLayout) > edits && reused == 0 {
				t.Fatalf("K07[%d] small edit (%d paths on %d chunks) reused 0 chunks (replaced=%d)", idx, edits, len(baseLayout), replaced)
			}
			chunkReuseOK++

		case 2:
			// Clean-up (Sweep) never removes any chunk referenced by a surviving manifest.
			tag := fmt.Sprintf("k07-%d", idx)
			oldTS := timelayout.Format(time.Now().Add(-2 * Retention))
			keptChunk := "kept-chunk-" + tag
			goneChunk := "gone-chunk-" + tag
			keptMan := "kept-man-" + tag
			goneMan := "gone-man-" + tag
			keptSnap := "kept-snap-" + tag
			goneSnap := "gone-snap-" + tag

			seedChunk(t, store, keptChunk, 4)
			seedChunk(t, store, goneChunk, 4)
			seedManifest(t, store, keptMan, keptChunk)
			seedManifest(t, store, goneMan, goneChunk)
			seedSnapshot(t, store, keptSnap, oldTS, keptMan)
			seedSnapshot(t, store, goneSnap, oldTS, goneMan)
			_, err := store.db.ExecContext(t.Context(),
				`INSERT INTO source_snapshot_heads (roots_key,snapshot_id,published_ts) VALUES (?,?,?)`,
				"live-"+tag, keptSnap, "2999-01-01T00:00:00Z")
			testutil.FailErr(t, "publish surviving head", err)

			report, _ := sweepCycle(t, store)
			if report.ChunksDeleted < 1 {
				t.Fatalf("K07[%d] sweep failed to reclaim unreferenced chunk %s", idx, goneChunk)
			}
			if got := countRows(t, store, fmt.Sprintf(`SELECT COUNT(*) FROM source_manifest_chunks WHERE id='%s'`, keptChunk)); got != 1 {
				t.Fatalf("K07[%d] sweep deleted surviving manifest chunk %s!", idx, keptChunk)
			}
			if got := countRows(t, store, fmt.Sprintf(`SELECT COUNT(*) FROM source_manifest_chunks WHERE id='%s'`, goneChunk)); got != 0 {
				t.Fatalf("K07[%d] sweep left unreferenced chunk %s behind", idx, goneChunk)
			}
			gcSafeOK++
		}

		passed++
	}

	raw, err := json.Marshal(cf03SnapSummary{
		Family:       "K07",
		Lane:         "in-process",
		BaseSeed:     cf03SnapBaseSeed,
		Platform:     runtime.GOOS + "/" + runtime.GOARCH,
		Planned:      planned,
		Executed:     passed,
		Passed:       passed,
		Failed:       0,
		DeclaredGaps: gapCounts,
		ChunkReuseOK: chunkReuseOK,
		GCSafeOK:     gcSafeOK,
	})
	testutil.FailErr(t, "marshal k07 summary", err)
	t.Logf("CF03_SUMMARY:%s", string(raw))
}
