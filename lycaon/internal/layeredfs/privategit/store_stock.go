package privategit

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git object and pack index format uses SHA-1
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const compactPackThreshold = 100_000

type compactPackIndex struct {
	fanout [256]uint32
	oids   []byte
}

func (c *compactPackIndex) count() int64 {
	if c == nil {
		return 0
	}
	return int64(c.fanout[255])
}

func (c *compactPackIndex) containsHex(oidHex string) bool {
	if c == nil || len(oidHex) != 40 {
		return false
	}
	var raw [20]byte
	if _, err := hex.Decode(raw[:], []byte(oidHex)); err != nil {
		return false
	}
	first := raw[0]
	var lo uint32
	if first > 0 {
		lo = c.fanout[first-1]
	}
	hi := c.fanout[first]
	for lo < hi {
		mid := lo + (hi-lo)/2
		off := int(mid) * 20
		cmp := bytes.Compare(c.oids[off:off+20], raw[:])
		if cmp == 0 {
			return true
		}
		if cmp < 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return false
}

func recordLooseObjectLocked(s *RetainedObjectStore, oid string, size int64) {
	oid = strings.ToLower(strings.TrimSpace(oid))
	if len(oid) != 40 {
		return
	}
	if prev, exists := s.stock.looseSizes[oid]; exists {
		s.stock.looseBytesTotal += size - prev
		s.stock.looseSizes[oid] = size
		return
	}
	s.stock.looseSizes[oid] = size
	s.stock.looseBytesTotal += size
	s.stock.logicalOIDRefs[oid]++
}

func removeLooseObjectLocked(s *RetainedObjectStore, oid string) {
	oid = strings.ToLower(strings.TrimSpace(oid))
	size, exists := s.stock.looseSizes[oid]
	if !exists {
		return
	}
	delete(s.stock.looseSizes, oid)
	s.stock.looseBytesTotal -= size
	if s.stock.logicalOIDRefs[oid] <= 1 {
		delete(s.stock.logicalOIDRefs, oid)
	} else {
		s.stock.logicalOIDRefs[oid]--
	}
}

func recordPackLocked(s *RetainedObjectStore, base string, packAndIdxBytes int64, oids map[string]struct{}, comp *compactPackIndex) {
	removePackLocked(s, base)
	if s.stock.compactPacks == nil {
		s.stock.compactPacks = make(map[string]*compactPackIndex)
	}
	if comp != nil {
		s.stock.compactPacks[base] = comp
		s.stock.compactObjectsTotal += comp.count()
	} else {
		cloned := make(map[string]struct{}, len(oids))
		for oid := range oids {
			norm := strings.ToLower(strings.TrimSpace(oid))
			if len(norm) == 40 {
				cloned[norm] = struct{}{}
				s.stock.logicalOIDRefs[norm]++
			}
		}
		s.stock.packOIDs[base] = cloned
	}
	s.stock.packSizes[base] = packAndIdxBytes
	s.stock.packBytesTotal += packAndIdxBytes
}

func removePackLocked(s *RetainedObjectStore, base string) {
	if comp, exists := s.stock.compactPacks[base]; exists {
		s.stock.compactObjectsTotal -= comp.count()
		delete(s.stock.compactPacks, base)
	}
	if members, exists := s.stock.packOIDs[base]; exists {
		for oid := range members {
			if s.stock.logicalOIDRefs[oid] <= 1 {
				delete(s.stock.logicalOIDRefs, oid)
			} else {
				s.stock.logicalOIDRefs[oid]--
			}
		}
		delete(s.stock.packOIDs, base)
	}
	if size, exists := s.stock.packSizes[base]; exists {
		s.stock.packBytesTotal -= size
		delete(s.stock.packSizes, base)
	}
}

func initStockFromDiskLocked(ctx context.Context, s *RetainedObjectStore) error {
	start := time.Now()
	prevStartup := s.stock.startupDuration
	if err := finalizeInterruptedPackRetirements(ctx, filepath.Join(s.objectsDir, "pack")); err != nil {
		return err
	}
	looseSizes, packSizes, packOIDs, compactPacks, _, err := scanStoreDiskState(ctx, s.rootDir, s.objectsDir)
	if err != nil {
		return err
	}
	dur := max(time.Since(start), time.Nanosecond)
	if prevStartup > 0 {
		dur = prevStartup
	}
	s.stock = storeStock{
		packOIDs: make(map[string]map[string]struct{}, len(packOIDs)), compactPacks: make(map[string]*compactPackIndex, len(compactPacks)),
		packSizes: make(map[string]int64, len(packSizes)), looseSizes: make(map[string]int64, len(looseSizes)),
		logicalOIDRefs: make(map[string]int, len(looseSizes)), startupDuration: dur,
	}
	for oid, sz := range looseSizes {
		recordLooseObjectLocked(s, oid, sz)
	}
	for base, sz := range packSizes {
		recordPackLocked(s, base, sz, packOIDs[base], compactPacks[base])
	}
	return nil
}

// InspectStock performs an explicit, cancellable on-disk qualification audit of the
// retained object store outside the routine O(1) Metrics() hot path (#1671).
func (s *RetainedObjectStore) InspectStock(ctx context.Context) (StockInventory, error) {
	if err := ctx.Err(); err != nil {
		return StockInventory{Complete: false}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, _, _, inv, err := scanStoreDiskState(ctx, s.rootDir, s.objectsDir)
	if err != nil {
		return StockInventory{Complete: false}, err
	}
	return inv, nil
}

func scanStoreDiskState(
	ctx context.Context, rootDir, objectsDir string,
) (map[string]int64, map[string]int64, map[string]map[string]struct{}, map[string]*compactPackIndex, StockInventory, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, nil, StockInventory{}, err
	}
	entries, err := os.ReadDir(objectsDir)
	if err != nil {
		return nil, nil, nil, nil, StockInventory{}, fmt.Errorf("privategit: read store objects dir: %w", err)
	}
	looseSizes, packSizes := make(map[string]int64), make(map[string]int64)
	packOIDs, uniqueOIDs := make(map[string]map[string]struct{}), make(map[string]struct{})
	compactPacks := make(map[string]*compactPackIndex)
	var compactTotal int64
	var inv StockInventory
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, nil, StockInventory{}, err
		}
		name := entry.Name()
		if !entry.IsDir() || name == "info" {
			continue
		}
		if name == "pack" {
			if err := scanPackDir(ctx, rootDir, filepath.Join(objectsDir, "pack"), packSizes, packOIDs, compactPacks, uniqueOIDs, &compactTotal, &inv); err != nil {
				return nil, nil, nil, nil, StockInventory{}, err
			}
			continue
		}
		if len(name) != 2 || !isHexString(name) {
			continue
		}
		shardDir := filepath.Join(objectsDir, name)
		sEntries, sErr := os.ReadDir(shardDir)
		if sErr != nil {
			return nil, nil, nil, nil, StockInventory{}, fmt.Errorf("privategit: read loose shard %s: %w", shardDir, sErr)
		}
		for _, se := range sEntries {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, nil, StockInventory{}, err
			}
			rest := se.Name()
			if se.IsDir() || len(rest) != 38 || !isHexString(rest) {
				continue
			}
			loosePath := filepath.Join(shardDir, rest)
			info, iErr := os.Stat(loosePath)
			if iErr != nil {
				return nil, nil, nil, nil, StockInventory{}, fmt.Errorf("privategit: stat loose object %s: %w", loosePath, iErr)
			}
			if !info.Mode().IsRegular() || info.Size() <= 0 {
				return nil, nil, nil, nil, StockInventory{}, fmt.Errorf("%w: invalid loose object %s", ErrCorruptObjectStore, loosePath)
			}
			f, oErr := os.Open(loosePath)
			if oErr != nil {
				return nil, nil, nil, nil, StockInventory{}, fmt.Errorf("privategit: open loose object %s: %w", loosePath, oErr)
			}
			_ = f.Close()
			oid := strings.ToLower(name + rest)
			looseSizes[oid], uniqueOIDs[oid] = info.Size(), struct{}{}
			inv.LooseObjects++
			inv.LooseBytes += info.Size()
		}
	}
	inv.LogicalObjects, inv.StoredBytes, inv.Complete = int64(len(uniqueOIDs))+compactTotal, inv.LooseBytes+inv.PackBytes, true
	return looseSizes, packSizes, packOIDs, compactPacks, inv, nil
}

func scanPackDir(
	ctx context.Context, rootDir, packDir string,
	packSizes map[string]int64, packOIDs map[string]map[string]struct{},
	compactPacks map[string]*compactPackIndex,
	uniqueOIDs map[string]struct{}, compactTotal *int64, inv *StockInventory,
) error {
	pEntries, err := os.ReadDir(packDir)
	if err != nil {
		return fmt.Errorf("privategit: read pack dir %s: %w", packDir, err)
	}
	retiringBases := collectRetiringPackBases(pEntries)
	seenBases, hasPack, hasIdx := make(map[string]struct{}), make(map[string]int64), make(map[string]int64)
	for _, pe := range pEntries {
		if err := ctx.Err(); err != nil {
			return err
		}
		ext := filepath.Ext(pe.Name())
		if pe.IsDir() || !strings.HasPrefix(pe.Name(), "pack-") || (ext != ".pack" && ext != ".idx") {
			continue
		}
		base := strings.TrimSuffix(pe.Name(), ext)
		if _, retiring := retiringBases[base]; retiring {
			continue
		}
		info, iErr := os.Stat(filepath.Join(packDir, pe.Name()))
		if iErr != nil {
			return fmt.Errorf("privategit: stat pack file %s: %w", pe.Name(), iErr)
		}
		seenBases[base] = struct{}{}
		if ext == ".pack" {
			hasPack[base] = info.Size()
		} else {
			hasIdx[base] = info.Size()
		}
	}
	for base := range seenBases {
		if err := ctx.Err(); err != nil {
			return err
		}
		pSize, okP := hasPack[base]
		iSize, okI := hasIdx[base]
		if !okP || !okI {
			return fmt.Errorf("%w: incomplete pack/idx pair for %s", ErrCorruptObjectStore, base)
		}
		oids, comp, err := parsePackAndIndexOIDs(ctx, rootDir, filepath.Join(packDir, base+".pack"), filepath.Join(packDir, base+".idx"), pSize)
		if err != nil {
			return err
		}
		packSizes[base] = pSize + iSize
		inv.PackBytes += pSize + iSize
		if comp != nil {
			compactPacks[base] = comp
			inv.PackedObjects += comp.count()
			*compactTotal += comp.count()
		} else {
			packOIDs[base] = oids
			inv.PackedObjects += int64(len(oids))
			for oid := range oids {
				uniqueOIDs[oid] = struct{}{}
			}
		}
	}
	return nil
}

func collectRetiringPackBases(entries []os.DirEntry) map[string]struct{} {
	retiringBases := make(map[string]struct{})
	for _, pe := range entries {
		if pe.IsDir() || !strings.HasPrefix(pe.Name(), retiringPackPrefix) {
			continue
		}
		trimmed := strings.TrimPrefix(pe.Name(), retiringPackPrefix)
		ext := filepath.Ext(trimmed)
		if ext == ".pack" || ext == ".idx" || ext == ".rev" {
			retiringBases[strings.TrimSuffix(trimmed, ext)] = struct{}{}
		}
	}
	return retiringBases
}

// finalizeInterruptedPackRetirements reconciles any pack retirement interrupted mid-GC:
// if a pack's retirement was started (.retiring-pack-<base>.* exists), any remaining live
// companion files for <base> and the .retiring-pack-<base>.* staging files are removed
// before scanning live pack/idx pairs.
func finalizeInterruptedPackRetirements(ctx context.Context, packDir string) error {
	entries, err := os.ReadDir(packDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("privategit: read pack dir %s: %w", packDir, err)
	}
	retiringBases := collectRetiringPackBases(entries)
	if len(retiringBases) == 0 {
		return nil
	}
	if info, statErr := os.Stat(packDir); statErr == nil {
		origPerm := info.Mode().Perm()
		if origPerm&0o200 == 0 {
			_ = os.Chmod(packDir, 0o755)
			defer func() { _ = os.Chmod(packDir, origPerm) }()
		}
	}
	sortedBases := make([]string, 0, len(retiringBases))
	for base := range retiringBases {
		sortedBases = append(sortedBases, base)
	}
	sort.Strings(sortedBases)

	for _, base := range sortedBases {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, ext := range []string{".pack", ".idx", ".rev"} {
			p := filepath.Join(packDir, base+ext)
			if err := removeStoreFile(ctx, p); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("privategit: finalize interrupted pack retirement %s: %w", p, err)
			}
		}
	}
	if err := syncStoreDirectoryCtx(ctx, packDir); err != nil {
		return fmt.Errorf("privategit: sync live companion retirement in %s: %w", packDir, err)
	}
	for _, base := range sortedBases {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, ext := range []string{".pack", ".idx", ".rev"} {
			p := filepath.Join(packDir, retiringPackPrefix+base+ext)
			if err := removeStoreFile(ctx, p); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("privategit: finalize interrupted pack retirement %s: %w", p, err)
			}
		}
	}
	if err := syncStoreDirectoryCtx(ctx, packDir); err != nil {
		return fmt.Errorf("privategit: sync tombstone retirement in %s: %w", packDir, err)
	}
	return nil
}

func parsePackAndIndexOIDs(ctx context.Context, workDir, packPath, idxPath string, packSize int64) (map[string]struct{}, *compactPackIndex, error) {
	if packSize < 32 {
		return nil, nil, fmt.Errorf("%w: truncated packfile %s (size %d)", ErrCorruptObjectStore, packPath, packSize)
	}
	pf, err := os.Open(packPath)
	if err != nil {
		return nil, nil, fmt.Errorf("privategit: open packfile %s: %w", packPath, err)
	}
	var hdr [12]byte
	_, rErr := pf.Read(hdr[:])
	_ = pf.Close()
	if rErr != nil || string(hdr[:4]) != "PACK" {
		return nil, nil, fmt.Errorf("%w: invalid pack header in %s", ErrCorruptObjectStore, packPath)
	}
	oids, comp, err := parsePackIndexOIDs(ctx, workDir, idxPath)
	if err != nil {
		return nil, nil, err
	}
	idxCount := uint32(len(oids))
	if comp != nil {
		idxCount = uint32(comp.count())
	}
	if idxCount != binary.BigEndian.Uint32(hdr[8:12]) {
		return nil, nil, fmt.Errorf("%w: pack %s object count mismatches index count %d", ErrCorruptObjectStore, packPath, idxCount)
	}
	return oids, comp, nil
}

func parsePackIndexOIDs(ctx context.Context, workDir, idxPath string) (map[string]struct{}, *compactPackIndex, error) {
	raw, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, nil, err
	}
	if len(raw) < 8 {
		return nil, nil, fmt.Errorf("%w: truncated pack index %s", ErrCorruptObjectStore, idxPath)
	}
	if raw[0] == 0xff && raw[1] == 't' && raw[2] == 'O' && raw[3] == 'c' && binary.BigEndian.Uint32(raw[4:8]) == 2 {
		if len(raw) < 8+256*4+40 {
			return nil, nil, fmt.Errorf("%w: truncated v2 pack index %s", ErrCorruptObjectStore, idxPath)
		}
		var fanout [256]uint32
		var prev uint32
		for i := 0; i < 256; i++ {
			cur := binary.BigEndian.Uint32(raw[8+i*4 : 12+i*4])
			if cur < prev {
				return nil, nil, fmt.Errorf("%w: corrupt fanout table in %s", ErrCorruptObjectStore, idxPath)
			}
			fanout[i] = cur
			prev = cur
		}
		count := int64(prev)
		if int64(len(raw)) < int64(8+256*4)+count*28+40 {
			return nil, nil, fmt.Errorf("%w: truncated v2 pack index %s", ErrCorruptObjectStore, idxPath)
		}
		sum := sha1.Sum(raw[:len(raw)-20]) //nolint:gosec // Git pack index trailer is SHA-1
		if !bytes.Equal(sum[:], raw[len(raw)-20:]) {
			return nil, nil, fmt.Errorf("%w: pack index checksum mismatch in %s", ErrCorruptObjectStore, idxPath)
		}
		if count > compactPackThreshold {
			oidStart := 8 + 256*4
			oidEnd := oidStart + int(count*20)
			oidBytes := make([]byte, int(count*20))
			copy(oidBytes, raw[oidStart:oidEnd])
			return nil, &compactPackIndex{fanout: fanout, oids: oidBytes}, nil
		}
		oids := make(map[string]struct{}, int(count))
		for i := int64(0); i < count; i++ {
			off := 8 + 256*4 + int(i*20)
			oids[hex.EncodeToString(raw[off:off+20])] = struct{}{}
		}
		return oids, nil, nil
	}
	oids, err := listPackIndexOIDs(ctx, workDir, idxPath)
	return oids, nil, err
}
