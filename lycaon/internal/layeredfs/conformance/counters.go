package conformance

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// SnapshotCoreCounters captures a point-in-time deterministic counter snapshot (#1667).
func SnapshotCoreCounters(
	core *layeredfs.Core,
	retained *privategit.RetainedObjectStore,
	rpcCount, handleLookups, cacheEntries, lowerGitConfigTouches int64,
) WorkResourceCounters {
	var stats layeredfs.Stats
	if core != nil {
		stats = core.Stats()
	}
	var retainedCount int64
	if retained != nil {
		// The store exposes cumulative import/reuse flows, not a stock count;
		// zero flows at claim time is the deterministic signal that nothing
		// was retained during preparation.
		m := retained.Metrics()
		retainedCount = m.ObjectsImported + m.ObjectsReused
	}
	return WorkResourceCounters{
		LowerDirReads:         stats.LowerDirReads,
		CapturedBytes:         stats.CapturedBaseBytes,
		ChangedNodes:          stats.LogicalChanges,
		PhysicalCopyUps:       stats.PhysicalCopyUps,
		RPCCount:              rpcCount,
		HandleLookups:         handleLookups,
		CacheEntries:          cacheEntries,
		RetainedObjects:       retainedCount,
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
		RetainedObjects:       after.RetainedObjects - before.RetainedObjects,
		LowerGitConfigTouches: after.LowerGitConfigTouches - before.LowerGitConfigTouches,
	}
}

// AssertPreparationBounds verifies mounted claim invariants: zero lower directory reads,
// zero physical copy-ups, zero changed nodes, zero retained objects, and zero lower Git config touches (#1667).
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
	if c.RetainedObjects != 0 {
		return fmt.Errorf("mounted preparation imported %d retained git objects at claim time; want 0", c.RetainedObjects)
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
type virtualScaleLowerProvider struct {
	logicalEntryCount int64
	dirReads          atomic.Int64
	lookups           atomic.Int64
}

func newVirtualScaleLowerProvider(logicalEntryCount int64) *virtualScaleLowerProvider {
	return &virtualScaleLowerProvider{logicalEntryCount: logicalEntryCount}
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
	if strings.HasPrefix(rel, "src/file-") || rel == "README.md" {
		return layeredfs.LowerEntry{
			Kind:    layeredfs.EntryRegular,
			Mode:    0o644,
			Size:    16,
			ModTime: now,
			Ino:     42,
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
		return []layeredfs.LowerDirEntry{
			{Name: "file-0.go", Kind: layeredfs.EntryRegular, Mode: 0o644, Ino: 10},
			{Name: "file-1.go", Kind: layeredfs.EntryRegular, Mode: 0o644, Ino: 11},
		}, nil
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
