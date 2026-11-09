package privategit

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/bloblifecycle"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fssync"
)

func storeLifecycleDataDir(rootDir string) string {
	clean := filepath.Clean(rootDir)
	for cur := clean; ; {
		parent := filepath.Dir(cur)
		if parent == cur {
			return clean
		}
		base := filepath.Base(cur)
		if base == enginepaths.WorkerBranchesDirName || base == enginepaths.WorkerRecoveryDirName {
			return parent
		}
		cur = parent
	}
}

// HasObject reports whether oid (40-character hex SHA-1 or "pack:<base>" pack reference)
// exists in the retained store either as a loose object or inside a retained packfile.
func (s *RetainedObjectStore) HasObject(oid string) bool {
	oid = strings.ToLower(strings.TrimSpace(oid))
	if strings.HasPrefix(oid, "pack:") {
		base := strings.TrimPrefix(oid, "pack:")
		base = strings.TrimSuffix(strings.TrimSuffix(base, ".pack"), ".idx")
		s.mu.Lock()
		_, ok := s.stock.packOIDs[base]
		s.mu.Unlock()
		return ok
	}
	if len(oid) != 40 || !isHexString(oid) {
		return false
	}
	s.mu.Lock()
	if s.stock.logicalOIDRefs[oid] > 0 {
		s.mu.Unlock()
		return true
	}
	for _, members := range s.stock.packOIDs {
		if _, ok := members[oid]; ok {
			s.mu.Unlock()
			return true
		}
	}
	s.mu.Unlock()
	loosePath := filepath.Join(s.objectsDir, oid[:2], oid[2:])
	_, err := os.Stat(loosePath)
	return err == nil
}

// CollectGarbage removes unreferenced loose objects and packfiles from the retained store
// while preserving every object and pack reachable from any active lease (#1671).
func (s *RetainedObjectStore) CollectGarbage(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	lifecycle := bloblifecycle.ForDevice(storeLifecycleDataDir(s.rootDir))
	lifecycle.Lock()
	defer lifecycle.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	unlockStoreWriteBoundary(s)
	// A store left with nothing retained has nothing to protect, as at open.
	defer func() {
		if storeHoldsObjects(s.objectsDir) {
			lockStoreReadOnlyBoundary(s)
		}
	}()

	leasedOIDs := make(map[string]struct{})
	leasedPacks := make(map[string]struct{})
	for _, l := range s.leases {
		for oid := range l.oids {
			leasedOIDs[oid] = struct{}{}
			for packName, members := range s.stock.packOIDs {
				if _, ok := members[oid]; ok {
					leasedPacks[packName] = struct{}{}
				}
			}
		}
		for p := range l.packs {
			leasedPacks[p] = struct{}{}
		}
	}

	removed := 0
	entries, err := os.ReadDir(s.objectsDir)
	if err != nil {
		return 0, err
	}
	for _, fanout := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		name := fanout.Name()
		if !fanout.IsDir() || len(name) != 2 || !isHexString(name) {
			continue
		}
		subDir := filepath.Join(s.objectsDir, name)
		files, err := os.ReadDir(subDir)
		if err != nil {
			return removed, err
		}
		for _, f := range files {
			if f.IsDir() || len(f.Name()) != 38 || !isHexString(f.Name()) {
				continue
			}
			oid := strings.ToLower(name + f.Name())
			if _, keep := leasedOIDs[oid]; keep {
				continue
			}
			loosePath := filepath.Join(subDir, f.Name())
			if err := removeStoreFile(ctx, loosePath); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return removed, fmt.Errorf("privategit: remove unreferenced loose object %s: %w", loosePath, err)
			}
			removeLooseObjectLocked(s, oid)
			removed++
		}
		if remaining, rErr := os.ReadDir(subDir); rErr == nil && len(remaining) == 0 {
			_ = os.Remove(subDir)
		}
	}

	packDir := filepath.Join(s.objectsDir, "pack")
	removedPacks, err := collectUnleasedPacksLocked(ctx, s, packDir, leasedPacks)
	removed += removedPacks
	return removed, err
}

const retiringPackPrefix = ".retiring-"

type gcTestHooksKey struct{}

type gcTestHooks struct {
	beforeStageRename         func(base, ext string) error
	beforeUnlinkRetired       func(base, ext string) error
	beforeRollbackMarkerWrite func(dir, base string) error
	beforeRollbackUnlink      func(path string) error
	removeFile                func(path string) error
	syncDir                   func(dirPath string) error
}

func withGCTestHooks(ctx context.Context, hooks gcTestHooks) context.Context {
	return context.WithValue(ctx, gcTestHooksKey{}, hooks)
}

func gcBeforeRollbackMarkerWrite(ctx context.Context, dir, base string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h, ok := ctx.Value(gcTestHooksKey{}).(gcTestHooks); ok && h.beforeRollbackMarkerWrite != nil {
		return h.beforeRollbackMarkerWrite(dir, base)
	}
	return nil
}

func gcBeforeRollbackUnlink(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h, ok := ctx.Value(gcTestHooksKey{}).(gcTestHooks); ok && h.beforeRollbackUnlink != nil {
		return h.beforeRollbackUnlink(path)
	}
	return nil
}

func writeDurablePackRetirementMarker(packDstDir, base string) error {
	tombstone := filepath.Join(packDstDir, retiringPackPrefix+base+".pack")
	f, err := os.OpenFile(tombstone, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o444)
	if err != nil {
		return err
	}
	syncErr := fssync.File(f)
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func gcBeforeStageRename(ctx context.Context, base, ext string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h, ok := ctx.Value(gcTestHooksKey{}).(gcTestHooks); ok && h.beforeStageRename != nil {
		return h.beforeStageRename(base, ext)
	}
	return nil
}

func gcBeforeUnlinkRetired(ctx context.Context, base, ext string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h, ok := ctx.Value(gcTestHooksKey{}).(gcTestHooks); ok && h.beforeUnlinkRetired != nil {
		return h.beforeUnlinkRetired(base, ext)
	}
	return nil
}

func removeStoreFile(ctx context.Context, path string) error {
	if h, ok := ctx.Value(gcTestHooksKey{}).(gcTestHooks); ok && h.removeFile != nil {
		return h.removeFile(path)
	}
	return os.Remove(path)
}

func syncStoreDirectory(dirPath string) error {
	dir, err := os.Open(dirPath)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return fssync.File(dir)
}

func syncStoreDirectoryCtx(ctx context.Context, dirPath string) error {
	if h, ok := ctx.Value(gcTestHooksKey{}).(gcTestHooks); ok && h.syncDir != nil {
		return h.syncDir(dirPath)
	}
	return syncStoreDirectory(dirPath)
}

func collectUnleasedPacksLocked(
	ctx context.Context,
	s *RetainedObjectStore,
	packDir string,
	leasedPacks map[string]struct{},
) (int, error) {
	if err := finalizeInterruptedPackRetirements(ctx, packDir); err != nil {
		return 0, err
	}
	packEntries, err := os.ReadDir(packDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	// Collect candidate unleased pack bases.
	unleasedBases := make(map[string]struct{})
	for _, pe := range packEntries {
		if pe.IsDir() || !strings.HasPrefix(pe.Name(), "pack-") {
			continue
		}
		ext := filepath.Ext(pe.Name())
		if ext != ".pack" && ext != ".idx" && ext != ".rev" {
			continue
		}
		base := strings.TrimSuffix(pe.Name(), ext)
		if _, keep := leasedPacks[base]; !keep {
			unleasedBases[base] = struct{}{}
		}
	}
	type stagedFile struct {
		ext string
		src string
		dst string
	}
	rollbackStaged := func(staged []stagedFile) bool {
		allRestored := true
		for i := len(staged) - 1; i >= 0; i-- {
			if err := os.Rename(staged[i].dst, staged[i].src); err != nil && !os.IsNotExist(err) {
				allRestored = false
			}
		}
		if len(staged) > 0 {
			if err := syncStoreDirectory(packDir); err != nil {
				allRestored = false
			}
		}
		return allRestored
	}
	// Retire each unleased pack in two phases: rename pack-* -> .retiring-pack-* first so no
	// live pack-<hash> member remains partially present, then unlink the retired files.
	for base := range unleasedBases {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		var staged []stagedFile
		hasPack := false
		for _, ext := range []string{".pack", ".idx", ".rev"} {
			if err := gcBeforeStageRename(ctx, base, ext); err != nil {
				if len(staged) > 0 {
					_ = syncStoreDirectory(packDir)
					removePackLocked(s, base)
				}
				return removed, err
			}
			src := filepath.Join(packDir, base+ext)
			dst := filepath.Join(packDir, retiringPackPrefix+base+ext)
			if err := os.Rename(src, dst); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				if rollbackStaged(staged) {
					return removed, fmt.Errorf("privategit: stage pack retirement %s (rolled back cleanly): %w", src, err)
				}
				removePackLocked(s, base)
				return removed, fmt.Errorf("privategit: stage pack retirement %s (rollback incomplete, marked for retirement): %w", src, err)
			}
			if ext == ".pack" {
				hasPack = true
			}
			staged = append(staged, stagedFile{ext: ext, src: src, dst: dst})
		}
		if len(staged) > 0 {
			if err := syncStoreDirectory(packDir); err != nil {
				removePackLocked(s, base)
				return removed, fmt.Errorf("privategit: sync staged pack retirement %s: %w", base, err)
			}
		}
		unlinkedAny := false
		for _, item := range staged {
			if err := gcBeforeUnlinkRetired(ctx, base, item.ext); err != nil {
				_ = syncStoreDirectory(packDir)
				removePackLocked(s, base)
				return removed, err
			}
			if err := removeStoreFile(ctx, item.dst); err != nil && !os.IsNotExist(err) {
				if !unlinkedAny && rollbackStaged(staged) {
					return removed, fmt.Errorf("privategit: remove retired pack %s (rolled back cleanly): %w", item.dst, err)
				}
				_ = syncStoreDirectory(packDir)
				removePackLocked(s, base)
				return removed, fmt.Errorf("privategit: remove retired pack %s (partially retired, marked for reconciliation): %w", item.dst, err)
			}
			unlinkedAny = true
		}
		if len(staged) > 0 {
			if err := syncStoreDirectory(packDir); err != nil {
				removePackLocked(s, base)
				return removed, fmt.Errorf("privategit: sync retired pack removal %s: %w", base, err)
			}
		}
		removePackLocked(s, base)
		if hasPack {
			removed++
		}
	}
	return removed, nil
}

// exportStoreObjectsToTrunk copies missing loose and packed objects from the retained store
// into dstObjectsDir so imported refs are self-contained in the trunk repository.
func exportStoreObjectsToTrunk(s *RetainedObjectStore, dstObjectsDir string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var copiedCount int64
	entries, err := os.ReadDir(s.objectsDir)
	if err != nil {
		return 0, err
	}
	for _, fanout := range entries {
		name := fanout.Name()
		if !fanout.IsDir() || len(name) != 2 || !isHexString(name) {
			continue
		}
		srcSub := filepath.Join(s.objectsDir, name)
		dstSub := filepath.Join(dstObjectsDir, name)
		files, err := os.ReadDir(srcSub)
		if err != nil {
			return copiedCount, err
		}
		for _, f := range files {
			if f.IsDir() || len(f.Name()) != 38 || !isHexString(f.Name()) {
				continue
			}
			dstFile := filepath.Join(dstSub, f.Name())
			if _, err := os.Stat(dstFile); err == nil {
				continue
			}
			if err := os.MkdirAll(dstSub, 0o755); err != nil {
				return copiedCount, err
			}
			if err := copyWritableObjectFile(filepath.Join(srcSub, f.Name()), dstFile); err != nil {
				return copiedCount, err
			}
			copiedCount++
		}
	}

	srcPackDir := filepath.Join(s.objectsDir, "pack")
	dstPackDir := filepath.Join(dstObjectsDir, "pack")
	packEntries, err := os.ReadDir(srcPackDir)
	if err != nil && !os.IsNotExist(err) {
		return copiedCount, err
	}
	for _, pe := range packEntries {
		if pe.IsDir() || !strings.HasPrefix(pe.Name(), "pack-") {
			continue
		}
		dstFile := filepath.Join(dstPackDir, pe.Name())
		if _, err := os.Stat(dstFile); err == nil {
			continue
		}
		if err := os.MkdirAll(dstPackDir, 0o755); err != nil {
			return copiedCount, err
		}
		if err := copyWritableObjectFile(filepath.Join(srcPackDir, pe.Name()), dstFile); err != nil {
			return copiedCount, err
		}
		if strings.HasSuffix(pe.Name(), ".pack") {
			copiedCount++
		}
	}
	return copiedCount, nil
}

func copyWritableObjectFile(src, dst string) error {
	_, _, err := copyFileAtomicReadOnly(context.Background(), src, dst)
	return err
}

func copyFileAtomicReadOnly(ctx context.Context, src, dst string) (int64, bool, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = in.Close() }()

	tmp := fmt.Sprintf("%s.tmp.%d", dst, os.Getpid())
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, false, err
	}
	n, copyErr := io.Copy(out, in)
	syncErr := fssync.File(out)
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return 0, false, copyErr
		}
		if syncErr != nil {
			return 0, false, syncErr
		}
		return 0, false, closeErr
	}
	if err := os.Chmod(tmp, 0o444); err != nil {
		_ = os.Remove(tmp)
		return 0, false, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, false, err
	}
	if err := syncStoreDirectoryCtx(ctx, filepath.Dir(dst)); err != nil {
		return 0, true, err
	}
	return n, true, nil
}

func isHexString(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}
