package privategit

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fssync"
)

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
			if err := os.Remove(filepath.Join(subDir, f.Name())); err == nil {
				removeLooseObjectLocked(s, oid)
				removed++
			}
		}
	}

	packDir := filepath.Join(s.objectsDir, "pack")
	packEntries, err := os.ReadDir(packDir)
	if err != nil && !os.IsNotExist(err) {
		return removed, err
	}
	// First pass: remove unleased .pack files before companion .idx/.rev files so an
	// interrupted GC never leaves an orphan .pack without .idx on disk.
	for _, pe := range packEntries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if pe.IsDir() || !strings.HasPrefix(pe.Name(), "pack-") || filepath.Ext(pe.Name()) != ".pack" {
			continue
		}
		base := strings.TrimSuffix(pe.Name(), ".pack")
		if _, keep := leasedPacks[base]; keep {
			continue
		}
		if err := os.Remove(filepath.Join(packDir, pe.Name())); err == nil {
			removePackLocked(s, base)
			removed++
		}
	}
	// Second pass: remove companion files (.idx, .rev, etc.) for unleased packs.
	for _, pe := range packEntries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if pe.IsDir() || !strings.HasPrefix(pe.Name(), "pack-") || filepath.Ext(pe.Name()) == ".pack" {
			continue
		}
		ext := filepath.Ext(pe.Name())
		base := strings.TrimSuffix(pe.Name(), ext)
		if _, keep := leasedPacks[base]; keep {
			continue
		}
		_ = os.Remove(filepath.Join(packDir, pe.Name()))
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
	_, err := copyFileAtomicReadOnly(src, dst)
	return err
}

func copyFileAtomicReadOnly(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer func() { _ = in.Close() }()

	tmp := fmt.Sprintf("%s.tmp.%d", dst, os.Getpid())
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(out, in)
	syncErr := fssync.File(out)
	closeErr := out.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return 0, copyErr
		}
		if syncErr != nil {
			return 0, syncErr
		}
		return 0, closeErr
	}
	if err := os.Chmod(tmp, 0o444); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	return n, nil
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
