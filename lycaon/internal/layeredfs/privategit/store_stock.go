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
	"strings"
)

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

func recordPackLocked(s *RetainedObjectStore, base string, packAndIdxBytes int64, oids map[string]struct{}) {
	removePackLocked(s, base)
	cloned := make(map[string]struct{}, len(oids))
	for oid := range oids {
		norm := strings.ToLower(strings.TrimSpace(oid))
		if len(norm) == 40 {
			cloned[norm] = struct{}{}
			s.stock.logicalOIDRefs[norm]++
		}
	}
	s.stock.packOIDs[base] = cloned
	s.stock.packSizes[base] = packAndIdxBytes
	s.stock.packBytesTotal += packAndIdxBytes
}

func removePackLocked(s *RetainedObjectStore, base string) {
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
	looseSizes, packSizes, packOIDs, _, err := scanStoreDiskState(ctx, s.rootDir, s.objectsDir)
	if err != nil {
		return err
	}
	s.stock = storeStock{
		packOIDs:       make(map[string]map[string]struct{}, len(packOIDs)),
		packSizes:      make(map[string]int64, len(packSizes)),
		looseSizes:     make(map[string]int64, len(looseSizes)),
		logicalOIDRefs: make(map[string]int, len(looseSizes)),
	}
	for oid, sz := range looseSizes {
		recordLooseObjectLocked(s, oid, sz)
	}
	for base, sz := range packSizes {
		recordPackLocked(s, base, sz, packOIDs[base])
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
	_, _, _, inv, err := scanStoreDiskState(ctx, s.rootDir, s.objectsDir)
	if err != nil {
		return StockInventory{Complete: false}, err
	}
	return inv, nil
}

func scanStoreDiskState(
	ctx context.Context, rootDir, objectsDir string,
) (map[string]int64, map[string]int64, map[string]map[string]struct{}, StockInventory, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, StockInventory{}, err
	}
	entries, err := os.ReadDir(objectsDir)
	if err != nil {
		return nil, nil, nil, StockInventory{}, fmt.Errorf("privategit: read store objects dir: %w", err)
	}
	looseSizes := make(map[string]int64)
	packSizes := make(map[string]int64)
	packOIDs := make(map[string]map[string]struct{})
	uniqueOIDs := make(map[string]struct{})
	var inv StockInventory

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, StockInventory{}, err
		}
		name := entry.Name()
		if !entry.IsDir() || name == "info" {
			continue
		}
		if name == "pack" {
			if err := scanPackDir(ctx, rootDir, filepath.Join(objectsDir, "pack"), packSizes, packOIDs, uniqueOIDs, &inv); err != nil {
				return nil, nil, nil, StockInventory{}, err
			}
			continue
		}
		if len(name) != 2 || !isHexString(name) {
			continue
		}
		shardDir := filepath.Join(objectsDir, name)
		sEntries, sErr := os.ReadDir(shardDir)
		if sErr != nil {
			return nil, nil, nil, StockInventory{}, fmt.Errorf("privategit: read loose shard %s: %w", shardDir, sErr)
		}
		for _, se := range sEntries {
			if err := ctx.Err(); err != nil {
				return nil, nil, nil, StockInventory{}, err
			}
			rest := se.Name()
			if se.IsDir() || len(rest) != 38 || !isHexString(rest) {
				continue
			}
			loosePath := filepath.Join(shardDir, rest)
			info, iErr := os.Stat(loosePath)
			if iErr != nil {
				return nil, nil, nil, StockInventory{}, fmt.Errorf("privategit: stat loose object %s: %w", loosePath, iErr)
			}
			if !info.Mode().IsRegular() || info.Size() <= 0 {
				return nil, nil, nil, StockInventory{}, fmt.Errorf("%w: invalid loose object %s", ErrCorruptObjectStore, loosePath)
			}
			f, oErr := os.Open(loosePath)
			if oErr != nil {
				return nil, nil, nil, StockInventory{}, fmt.Errorf("privategit: open loose object %s: %w", loosePath, oErr)
			}
			_ = f.Close()
			oid := strings.ToLower(name + rest)
			looseSizes[oid] = info.Size()
			uniqueOIDs[oid] = struct{}{}
			inv.LooseObjects++
			inv.LooseBytes += info.Size()
		}
	}
	inv.LogicalObjects = int64(len(uniqueOIDs))
	inv.StoredBytes = inv.LooseBytes + inv.PackBytes
	inv.Complete = true
	return looseSizes, packSizes, packOIDs, inv, nil
}

func scanPackDir(
	ctx context.Context, rootDir, packDir string,
	packSizes map[string]int64, packOIDs map[string]map[string]struct{},
	uniqueOIDs map[string]struct{}, inv *StockInventory,
) error {
	pEntries, err := os.ReadDir(packDir)
	if err != nil {
		return fmt.Errorf("privategit: read pack dir %s: %w", packDir, err)
	}
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
		oids, err := parsePackAndIndexOIDs(ctx, rootDir, filepath.Join(packDir, base+".pack"), filepath.Join(packDir, base+".idx"), pSize)
		if err != nil {
			return err
		}
		packSizes[base] = pSize + iSize
		packOIDs[base] = oids
		inv.PackBytes += pSize + iSize
		inv.PackedObjects += int64(len(oids))
		for oid := range oids {
			uniqueOIDs[oid] = struct{}{}
		}
	}
	return nil
}

func parsePackAndIndexOIDs(ctx context.Context, workDir, packPath, idxPath string, packSize int64) (map[string]struct{}, error) {
	if packSize < 32 {
		return nil, fmt.Errorf("%w: truncated packfile %s (size %d)", ErrCorruptObjectStore, packPath, packSize)
	}
	pf, err := os.Open(packPath)
	if err != nil {
		return nil, fmt.Errorf("privategit: open packfile %s: %w", packPath, err)
	}
	var hdr [12]byte
	_, rErr := pf.Read(hdr[:])
	_ = pf.Close()
	if rErr != nil || string(hdr[:4]) != "PACK" {
		return nil, fmt.Errorf("%w: invalid pack header in %s", ErrCorruptObjectStore, packPath)
	}
	oids, err := parsePackIndexOIDs(ctx, workDir, idxPath)
	if err != nil {
		return nil, err
	}
	if uint32(len(oids)) != binary.BigEndian.Uint32(hdr[8:12]) {
		return nil, fmt.Errorf("%w: pack %s object count mismatches index count %d", ErrCorruptObjectStore, packPath, len(oids))
	}
	return oids, nil
}

func parsePackIndexOIDs(ctx context.Context, workDir, idxPath string) (map[string]struct{}, error) {
	raw, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, err
	}
	if len(raw) < 8 {
		return nil, fmt.Errorf("%w: truncated pack index %s", ErrCorruptObjectStore, idxPath)
	}
	if raw[0] == 0xff && raw[1] == 't' && raw[2] == 'O' && raw[3] == 'c' && binary.BigEndian.Uint32(raw[4:8]) == 2 {
		if len(raw) < 8+256*4+40 {
			return nil, fmt.Errorf("%w: truncated v2 pack index %s", ErrCorruptObjectStore, idxPath)
		}
		var prev uint32
		for i := 0; i < 256; i++ {
			cur := binary.BigEndian.Uint32(raw[8+i*4 : 12+i*4])
			if cur < prev {
				return nil, fmt.Errorf("%w: corrupt fanout table in %s", ErrCorruptObjectStore, idxPath)
			}
			prev = cur
		}
		count := int64(prev)
		if int64(len(raw)) < int64(8+256*4)+count*28+40 {
			return nil, fmt.Errorf("%w: truncated v2 pack index %s", ErrCorruptObjectStore, idxPath)
		}
		sum := sha1.Sum(raw[:len(raw)-20]) //nolint:gosec // Git pack index trailer is SHA-1
		if !bytes.Equal(sum[:], raw[len(raw)-20:]) {
			return nil, fmt.Errorf("%w: pack index checksum mismatch in %s", ErrCorruptObjectStore, idxPath)
		}
		oids := make(map[string]struct{}, int(count))
		for i := int64(0); i < count; i++ {
			off := 8 + 256*4 + int(i*20)
			oids[hex.EncodeToString(raw[off:off+20])] = struct{}{}
		}
		return oids, nil
	}
	return listPackIndexOIDs(ctx, workDir, idxPath)
}
