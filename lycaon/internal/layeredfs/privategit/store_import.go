package privategit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/gitexec"
)

// ImportRepositoryObjects imports loose and packed objects from srcObjectDirs into the
// retained object store, verifies object integrity and commit connectivity for tipOIDs,
// and returns the import metrics and complete set of reachable OIDs + pack identifiers.
func (s *RetainedObjectStore) ImportRepositoryObjects(
	ctx context.Context,
	srcObjectDirs []string,
	tipOIDs []string,
) (InitMetrics, []string, error) {
	start := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	unlockStoreWriteBoundary(s)
	defer lockStoreReadOnlyBoundary(s)

	var metrics InitMetrics
	var addedPaths []string
	var packNames []string
	var addedPackBases []string
	looseAddedSizes := make(map[string]int64)

	rollback := func() error {
		var rbErrs []error
		failedPackBases := make(map[string]struct{})
		packDstDir := filepath.Join(s.objectsDir, "pack")
		for _, p := range addedPaths {
			dir := filepath.Dir(p)
			_ = os.Chmod(dir, 0o755)
			if rmErr := removeStoreFile(ctx, p); rmErr != nil && !os.IsNotExist(rmErr) {
				rbErrs = append(rbErrs, fmt.Errorf("rollback remove %s: %w", p, rmErr))
				if dir == packDstDir {
					ext := filepath.Ext(filepath.Base(p))
					base := strings.TrimSuffix(filepath.Base(p), ext)
					if strings.HasPrefix(base, "pack-") {
						failedPackBases[base] = struct{}{}
					}
				}
			} else if dir != packDstDir {
				if rem, rErr := os.ReadDir(dir); rErr == nil && len(rem) == 0 {
					_ = os.Remove(dir)
				}
			}
		}
		for _, base := range addedPackBases {
			removePackLocked(s, base)
		}
		for base := range failedPackBases {
			tombstone := filepath.Join(packDstDir, retiringPackPrefix+base+".pack")
			if err := os.WriteFile(tombstone, nil, 0o444); err != nil {
				rbErrs = append(rbErrs, fmt.Errorf("stage rollback retirement tombstone %s: %w", tombstone, err))
			}
		}
		if len(failedPackBases) > 0 {
			_ = syncStoreDirectory(packDstDir)
		}
		if stockErr := initStockFromDiskLocked(ctx, s); stockErr != nil {
			rbErrs = append(rbErrs, fmt.Errorf("rollback rescan stock: %w", stockErr))
		}
		return errors.Join(rbErrs...)
	}

	for _, srcDir := range srcObjectDirs {
		if err := ctx.Err(); err != nil {
			return InitMetrics{}, nil, errors.Join(err, rollback())
		}
		looseAdded, err := importStoreLooseFromDir(ctx, s, srcDir, &metrics, looseAddedSizes)
		addedPaths = append(addedPaths, looseAdded...)
		if err != nil {
			return InitMetrics{}, nil, errors.Join(err, rollback())
		}

		packsAdded, pNames, newBases, err := importStorePacksFromDir(ctx, s, srcDir, &metrics)
		addedPaths = append(addedPaths, packsAdded...)
		addedPackBases = append(addedPackBases, newBases...)
		if err != nil {
			return InitMetrics{}, nil, errors.Join(err, rollback())
		}
		packNames = append(packNames, pNames...)
	}

	reachable, err := verifyStoreConnectivity(ctx, s.objectsDir, tipOIDs)
	if err != nil {
		return InitMetrics{}, nil, errors.Join(fmt.Errorf("%w: %v", ErrCorruptObjectStore, err), rollback())
	}
	for oid, sz := range looseAddedSizes {
		recordLooseObjectLocked(s, oid, sz)
	}
	for _, pName := range packNames {
		reachable = append(reachable, "pack:"+pName)
	}

	s.objectsImported.Add(metrics.ObjectsImported)
	s.objectsReused.Add(metrics.ObjectsReused)
	s.bytesImported.Add(metrics.BytesImported)
	s.bytesReused.Add(metrics.BytesReused)
	metrics.Duration = time.Since(start)
	metrics.State = StateReady
	return metrics, reachable, nil
}

func importStoreLooseFromDir(ctx context.Context, s *RetainedObjectStore, srcDir string, m *InitMetrics, looseAddedSizes map[string]int64) ([]string, error) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var added []string
	for _, fanout := range entries {
		name := fanout.Name()
		if !fanout.IsDir() || len(name) != 2 || !isHexString(name) {
			continue
		}
		subSrc := filepath.Join(srcDir, name)
		subDst := filepath.Join(s.objectsDir, strings.ToLower(name))
		files, err := os.ReadDir(subSrc)
		if err != nil {
			return added, err
		}
		for _, f := range files {
			if f.IsDir() || len(f.Name()) != 38 || !isHexString(f.Name()) {
				continue
			}
			oid := strings.ToLower(name + f.Name())
			srcFile := filepath.Join(subSrc, f.Name())
			dstFile := filepath.Join(subDst, strings.ToLower(f.Name()))
			info, sErr := os.Stat(srcFile)
			if sErr != nil {
				return added, sErr
			}
			if dInfo, dErr := os.Stat(dstFile); dErr == nil {
				m.ObjectsReused++
				m.BytesReused += info.Size()
				if _, known := s.stock.looseSizes[oid]; !known {
					looseAddedSizes[oid] = dInfo.Size()
				}
				continue
			}
			if err := os.MkdirAll(subDst, 0o755); err != nil {
				return added, err
			}
			n, published, cErr := copyFileAtomicReadOnly(ctx, srcFile, dstFile)
			if published {
				added = append(added, dstFile)
			}
			if cErr != nil {
				return added, cErr
			}
			looseAddedSizes[oid] = n
			m.ObjectsImported++
			m.BytesImported += n
		}
	}
	return added, nil
}

func importStorePacksFromDir(ctx context.Context, s *RetainedObjectStore, srcDir string, m *InitMetrics) ([]string, []string, []string, error) {
	packSrcDir := filepath.Join(srcDir, "pack")
	entries, err := os.ReadDir(packSrcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}
	packDstDir := filepath.Join(s.objectsDir, "pack")
	if err := os.MkdirAll(packDstDir, 0o755); err != nil {
		return nil, nil, nil, err
	}
	if err := finalizeInterruptedPackRetirements(ctx, packDstDir); err != nil {
		return nil, nil, nil, err
	}
	var added []string
	var packNames []string
	var addedPackBases []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "pack-") {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".pack" && ext != ".idx" && ext != ".rev" {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ext)
		if ext == ".pack" {
			packNames = append(packNames, base)
		}
		srcFile := filepath.Join(packSrcDir, e.Name())
		dstFile := filepath.Join(packDstDir, e.Name())
		info, sErr := os.Stat(srcFile)
		if sErr != nil {
			return added, packNames, addedPackBases, sErr
		}
		if _, dErr := os.Stat(dstFile); dErr == nil {
			if ext == ".pack" {
				m.ObjectsReused++
			}
			m.BytesReused += info.Size()
			continue
		}
		n, published, cErr := copyFileAtomicReadOnly(ctx, srcFile, dstFile)
		if published {
			added = append(added, dstFile)
		}
		if cErr != nil {
			return added, packNames, addedPackBases, cErr
		}
		if ext == ".pack" {
			m.ObjectsImported++
		}
		m.BytesImported += n
	}
	for _, base := range packNames {
		if _, exists := s.stock.packOIDs[base]; !exists {
			packPath := filepath.Join(packDstDir, base+".pack")
			idxPath := filepath.Join(packDstDir, base+".idx")
			pInfo, pErr := os.Stat(packPath)
			iInfo, iErr := os.Stat(idxPath)
			if pErr != nil || iErr != nil {
				return added, packNames, addedPackBases, fmt.Errorf("%w: missing pack or index for %s", ErrCorruptObjectStore, base)
			}
			oids, err := parsePackAndIndexOIDs(ctx, s.rootDir, packPath, idxPath, pInfo.Size())
			if err != nil {
				return added, packNames, addedPackBases, fmt.Errorf("%w: verify pack %s: %v", ErrCorruptObjectStore, base, err)
			}
			addedPackBases = append(addedPackBases, base)
			recordPackLocked(s, base, pInfo.Size()+iInfo.Size(), oids)
		}
	}
	return added, packNames, addedPackBases, nil
}

func listPackIndexOIDs(ctx context.Context, workDir, idxPath string) (map[string]struct{}, error) {
	rawIdx, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, err
	}
	out, code, err := gitexec.Run(ctx, workDir, []string{"show-index"}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
		Input:   rawIdx,
	})
	if err != nil || code != 0 {
		return nil, fmt.Errorf("git show-index exit %d: %v (%s)", code, err, strings.TrimSpace(string(out)))
	}
	res := make(map[string]struct{})
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && len(fields[1]) == 40 && isHexString(fields[1]) {
			res[strings.ToLower(fields[1])] = struct{}{}
		}
	}
	return res, nil
}

func verifyStoreConnectivity(ctx context.Context, objectsDir string, tipOIDs []string) ([]string, error) {
	if len(tipOIDs) == 0 {
		return nil, nil
	}
	tmpRepo, err := os.MkdirTemp(filepath.Dir(objectsDir), "verify-repo-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmpRepo) }()

	if _, code, err := gitexec.Run(ctx, tmpRepo, []string{"init", "--bare", "-q"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err != nil || code != 0 {
		return nil, fmt.Errorf("init bare verify repo: %v", err)
	}
	altFile := filepath.Join(tmpRepo, "objects", "info", "alternates")
	if err := os.WriteFile(altFile, []byte(objectsDir+"\n"), 0o600); err != nil {
		return nil, err
	}
	var uniqueTips []string
	seenTip := make(map[string]struct{}, len(tipOIDs))
	for _, raw := range tipOIDs {
		oid := strings.ToLower(strings.TrimSpace(raw))
		if len(oid) == 40 && isHexString(oid) {
			if _, ok := seenTip[oid]; !ok {
				seenTip[oid] = struct{}{}
				uniqueTips = append(uniqueTips, oid)
			}
		}
	}
	if len(uniqueTips) == 0 {
		return nil, nil
	}
	args := append([]string{"rev-list", "--objects"}, uniqueTips...)
	out, code, err := gitexec.Run(ctx, tmpRepo, args, gitexec.Opts{Profile: gitexec.ProfileHermetic})
	if err != nil || code != 0 {
		return nil, fmt.Errorf("rev-list connectivity failed (exit %d): %v: %s", code, err, strings.TrimSpace(string(out)))
	}
	var reachable []string
	seen := make(map[string]struct{})
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) > 0 && len(fields[0]) == 40 {
			oid := strings.ToLower(string(fields[0]))
			if _, ok := seen[oid]; !ok {
				seen[oid] = struct{}{}
				reachable = append(reachable, oid)
			}
		}
	}
	for _, tip := range uniqueTips {
		if _, ok := seen[tip]; !ok {
			reachable = append(reachable, tip)
		}
	}
	return reachable, nil
}
