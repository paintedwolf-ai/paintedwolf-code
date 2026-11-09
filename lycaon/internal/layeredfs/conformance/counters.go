package conformance

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// SnapshotCoreCounters captures a point-in-time deterministic counter snapshot (#1667).
//
// Retained-store assumptions: Metrics() flows are cumulative since store creation.
// A snapshot is only meaningful against a specific store instance; deltas between
// two snapshots (DiffCounters) are the assertion unit. Baseline: a preexisting
// (nonempty) store starts from a nonzero baseline, so claim-time zero-ness is
// asserted only for freshly created stores. Reset: the store exposes no reset;
// tests must construct a new store per scenario. Concurrency: activity concurrent
// with the snapshot window inflates both deltas; tests must hold the store
// quiescent across the window or account for concurrent activity explicitly.
func SnapshotCoreCounters(
	core *layeredfs.Core,
	retained *privategit.RetainedObjectStore,
	rpcCount, handleLookups, cacheEntries, lowerGitConfigTouches int64,
) WorkResourceCounters {
	var stats layeredfs.Stats
	if core != nil {
		stats = core.Stats()
	}
	var imports, reuses int64
	if retained != nil {
		// Activity flows, not stock: the store exposes cumulative import/reuse
		// counters only (see WorkResourceCounters for the semantics contract).
		m := retained.Metrics()
		imports = m.ObjectsImported
		reuses = m.ObjectsReused
	}
	return WorkResourceCounters{
		LowerDirReads:         stats.LowerDirReads,
		CapturedBytes:         stats.CapturedBaseBytes,
		ChangedNodes:          stats.LogicalChanges,
		PhysicalCopyUps:       stats.PhysicalCopyUps,
		RPCCount:              rpcCount,
		HandleLookups:         handleLookups,
		CacheEntries:          cacheEntries,
		RetainedImports:       imports,
		RetainedReuses:        reuses,
		LowerGitConfigTouches: lowerGitConfigTouches,
	}
}

// DiffCounters computes the delta between two counter snapshots (#1667).
func DiffCounters(before, after WorkResourceCounters) WorkResourceCounters {
	return WorkResourceCounters{
		LowerDirReads:         after.LowerDirReads - before.LowerDirReads,
		CapturedBytes:         after.CapturedBytes - before.CapturedBytes,
		ChangedNodes:          after.ChangedNodes - before.ChangedNodes,
		PhysicalCopyUps:       after.PhysicalCopyUps - before.PhysicalCopyUps,
		RPCCount:              after.RPCCount - before.RPCCount,
		HandleLookups:         after.HandleLookups - before.HandleLookups,
		CacheEntries:          after.CacheEntries - before.CacheEntries,
		RetainedImports:       after.RetainedImports - before.RetainedImports,
		RetainedReuses:        after.RetainedReuses - before.RetainedReuses,
		LowerGitConfigTouches: after.LowerGitConfigTouches - before.LowerGitConfigTouches,
	}
}

// AssertPreparationBounds verifies mounted claim invariants: zero lower directory reads,
// zero physical copy-ups, zero changed nodes, zero retained-store import/reuse activity,
// and zero lower Git config touches (#1667).
// The retained assertions apply to a FRESHLY CREATED store (see SnapshotCoreCounters
// for baseline semantics): a preexisting store legitimately starts from a nonzero
// baseline, so claim-time zero-ness is asserted only there.
func AssertPreparationBounds(c WorkResourceCounters) error {
	if c.LowerDirReads != 0 {
		return fmt.Errorf("mounted preparation read %d lower directories; want 0", c.LowerDirReads)
	}
	if c.PhysicalCopyUps != 0 {
		return fmt.Errorf("mounted preparation triggered %d physical copy-ups; want 0", c.PhysicalCopyUps)
	}
	if c.ChangedNodes != 0 {
		return fmt.Errorf("mounted preparation recorded %d changed nodes; want 0", c.ChangedNodes)
	}
	if c.RetainedImports != 0 {
		return fmt.Errorf("mounted preparation imported %d retained git objects at claim time; want 0", c.RetainedImports)
	}
	if c.RetainedReuses != 0 {
		return fmt.Errorf("mounted preparation reused %d retained git objects at claim time; want 0", c.RetainedReuses)
	}
	if c.LowerGitConfigTouches != 0 {
		return fmt.Errorf("mounted preparation touched %d lower git configs; want 0", c.LowerGitConfigTouches)
	}
	return nil
}

// AssertScopedTurnBounds verifies that a scoped turn stays within expected directory read and
// copy-up bounds without requiring physical copy-ups to equal logical change counts (#1667).
func AssertScopedTurnBounds(c WorkResourceCounters, maxDirReads, maxCopyUps int64) error {
	if c.LowerDirReads > maxDirReads {
		return fmt.Errorf("scoped turn read %d lower directories; max allowed %d", c.LowerDirReads, maxDirReads)
	}
	if c.PhysicalCopyUps > maxCopyUps {
		return fmt.Errorf("scoped turn performed %d physical copy-ups; max allowed %d", c.PhysicalCopyUps, maxCopyUps)
	}
	if c.LowerGitConfigTouches != 0 {
		return fmt.Errorf("scoped turn touched %d lower git configs; want 0", c.LowerGitConfigTouches)
	}
	return nil
}

// virtualScaleLowerProvider synthesizes a virtual lower tree of arbitrary logical size
// (e.g. 1,000 vs 2,000,000 entries) so preparation scaling tests can prove that mounted claim
// performs 0 lower directory reads regardless of lower tree size (#1667).
//
// The fixture is faithful and bounded: the logical tree holds EXACTLY logicalEntryCount
// entries under src/ (src/file-0 … src/file-<N-1>). Lstat accepts only in-range names and
// returns os.ErrNotExist for out-of-range or non-numeric names; ReadDir("src") returns
// exactly logicalEntryCount entries, so distinct cardinalities are observable. This
// fixture qualifies the mounted claim's root-level operation count only — it is NOT a
// real 2M-entry filesystem workload; actual filesystem workloads are identified
// separately (#1667 contract).
type virtualScaleLowerProvider struct {
	logicalEntryCount int64
	dirReads          atomic.Int64
	lookups           atomic.Int64
}

func newVirtualScaleLowerProvider(logicalEntryCount int64) *virtualScaleLowerProvider {
	return &virtualScaleLowerProvider{logicalEntryCount: logicalEntryCount}
}

// srcFileEntry parses "src/file-<i>" and returns the index, ok=false for other names.
func srcFileEntry(rel string) (int64, bool) {
	const prefix = "src/file-"
	if !strings.HasPrefix(rel, prefix) {
		return 0, false
	}
	idx, err := strconv.ParseInt(rel[len(prefix):], 10, 64)
	if err != nil || idx < 0 {
		return 0, false
	}
	return idx, true
}

func (v *virtualScaleLowerProvider) Lstat(rel string) (layeredfs.LowerEntry, error) {
	v.lookups.Add(1)
	now := time.Unix(1700000000, 0).UTC()
	if rel == "" || rel == "." || rel == "src" || rel == "unrelated_dir" {
		return layeredfs.LowerEntry{
			Kind:    layeredfs.EntryDirectory,
			Mode:    fs.ModeDir | 0o755,
			ModTime: now,
			Ino:     1,
			Nlink:   2,
		}, nil
	}
	if rel == "README.md" {
		return layeredfs.LowerEntry{
			Kind:    layeredfs.EntryRegular,
			Mode:    0o644,
			Size:    16,
			ModTime: now,
			Ino:     42,
			Nlink:   1,
		}, nil
	}
	if idx, ok := srcFileEntry(rel); ok && idx < v.logicalEntryCount {
		return layeredfs.LowerEntry{
			Kind:    layeredfs.EntryRegular,
			Mode:    0o644,
			Size:    16,
			ModTime: now,
			Ino:     uint64(100 + idx),
			Nlink:   1,
		}, nil
	}
	return layeredfs.LowerEntry{}, os.ErrNotExist
}

func (v *virtualScaleLowerProvider) ReadDir(rel string) ([]layeredfs.LowerDirEntry, error) {
	v.dirReads.Add(1)
	if rel == "" || rel == "." {
		return []layeredfs.LowerDirEntry{
			{Name: "README.md", Kind: layeredfs.EntryRegular, Mode: 0o644, Ino: 2},
			{Name: "src", Kind: layeredfs.EntryDirectory, Mode: fs.ModeDir | 0o755, Ino: 3},
			{Name: "unrelated_dir", Kind: layeredfs.EntryDirectory, Mode: fs.ModeDir | 0o755, Ino: 4},
		}, nil
	}
	if rel == "src" {
		entries := make([]layeredfs.LowerDirEntry, v.logicalEntryCount)
		for i := int64(0); i < v.logicalEntryCount; i++ {
			entries[i] = layeredfs.LowerDirEntry{
				Name: fmt.Sprintf("file-%d", i),
				Kind: layeredfs.EntryRegular,
				Mode: 0o644,
				Ino:  uint64(100 + i),
			}
		}
		return entries, nil
	}
	return nil, os.ErrNotExist
}

func (v *virtualScaleLowerProvider) OpenRead(rel string) (layeredfs.ReadSeekCloser, layeredfs.LowerEntry, error) {
	entry, err := v.Lstat(rel)
	if err != nil {
		return nil, layeredfs.LowerEntry{}, err
	}
	data := []byte("package sample\n\n")
	return &bytesReadSeekCloser{Reader: bytes.NewReader(data)}, entry, nil
}

func (v *virtualScaleLowerProvider) Readlink(_ string) (string, error) {
	return "", os.ErrInvalid
}

func (v *virtualScaleLowerProvider) ListXattr(_ string) (map[string][]byte, error) {
	return nil, nil
}

type bytesReadSeekCloser struct {
	*bytes.Reader
}

func (b *bytesReadSeekCloser) Close() error { return nil }
