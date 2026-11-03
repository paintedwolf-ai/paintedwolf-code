package nfsserver

import (
	"bytes"
	"encoding/binary"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

const (
	appleDoubleMagic   uint32 = 0x00051607
	appleDoubleVersion uint32 = 0x00020000
	appleDoubleFinder  uint32 = 9
	appleDoubleAttrMag uint32 = 0x41545452 // "ATTR"
)

func isAppleDoublePath(relPath string) (string, bool) {
	base := path.Base(relPath)
	if !strings.HasPrefix(base, "._") || len(base) <= 2 {
		return "", false
	}
	targetName := strings.TrimPrefix(base, "._")
	dir := path.Dir(relPath)
	if dir == "." || dir == "" {
		return targetName, true
	}
	return dir + "/" + targetName, true
}

// EncodeAppleDoubleXattrs encodes Finder info and extended attributes into an
// AppleDouble v2 header (magic 0x00051607, version 0x00020000, entry 9 with ATTR table).
func EncodeAppleDoubleXattrs(finderInfo [32]byte, xattrs map[string][]byte) []byte {
	names := make([]string, 0, len(xattrs))
	for k := range xattrs {
		names = append(names, k)
	}
	sort.Strings(names)

	var attrPayload bytes.Buffer
	var hdr [12]byte
	binary.BigEndian.PutUint32(hdr[0:4], appleDoubleAttrMag)
	binary.BigEndian.PutUint32(hdr[4:8], uint32(len(names)))
	_, _ = attrPayload.Write(hdr[:])

	for _, name := range names {
		val := xattrs[name]
		writeUint32(&attrPayload, uint32(len(name)))
		_, _ = attrPayload.WriteString(name)
		writeUint32(&attrPayload, uint32(len(val)))
		_, _ = attrPayload.Write(val)
	}

	entryDataLen := uint32(32 + attrPayload.Len())
	const entryOffset = 26 + 12 // 26-byte AppleDouble header + 1x 12-byte entry descriptor.

	var out bytes.Buffer
	writeUint32(&out, appleDoubleMagic)
	writeUint32(&out, appleDoubleVersion)
	var filler [16]byte
	_, _ = out.Write(filler[:])
	var numEntries [2]byte
	binary.BigEndian.PutUint16(numEntries[:], 1)
	_, _ = out.Write(numEntries[:])

	writeUint32(&out, appleDoubleFinder)
	writeUint32(&out, entryOffset)
	writeUint32(&out, entryDataLen)

	_, _ = out.Write(finderInfo[:])
	_, _ = out.Write(attrPayload.Bytes())
	return out.Bytes()
}

// DecodeAppleDoubleXattrs parses an AppleDouble v2 payload produced by macOS or EncodeAppleDoubleXattrs.
func DecodeAppleDoubleXattrs(data []byte) ([32]byte, map[string][]byte, bool) {
	var finderInfo [32]byte
	if len(data) < 26 {
		return finderInfo, nil, false
	}
	if binary.BigEndian.Uint32(data[0:4]) != appleDoubleMagic ||
		binary.BigEndian.Uint32(data[4:8]) != appleDoubleVersion {
		return finderInfo, nil, false
	}
	numEntries := int(binary.BigEndian.Uint16(data[24:26]))
	if numEntries <= 0 || numEntries > 32 || len(data) < 26+numEntries*12 {
		return finderInfo, nil, false
	}
	for i := 0; i < numEntries; i++ {
		off := 26 + i*12
		entryID := binary.BigEndian.Uint32(data[off : off+4])
		entryOff := binary.BigEndian.Uint32(data[off+4 : off+8])
		entryLen := binary.BigEndian.Uint32(data[off+8 : off+12])
		if entryID != appleDoubleFinder {
			continue
		}
		if uint64(entryOff)+uint64(entryLen) > uint64(len(data)) || entryLen < 32 {
			return finderInfo, nil, false
		}
		section := data[entryOff : entryOff+entryLen]
		copy(finderInfo[:], section[0:32])
		out := make(map[string][]byte)
		if len(section) == 32 {
			return finderInfo, out, true
		}
		if parsed, ok := parseAppleDoubleAttrSection(section[32:]); ok {
			return finderInfo, parsed, true
		}
		return finderInfo, out, true
	}
	return finderInfo, nil, false
}

func parseAppleDoubleAttrSection(attrBytes []byte) (map[string][]byte, bool) {
	if len(attrBytes) < 12 || binary.BigEndian.Uint32(attrBytes[0:4]) != appleDoubleAttrMag {
		return nil, false
	}
	count := int(binary.BigEndian.Uint32(attrBytes[4:8]))
	if count < 0 || count > 256 {
		return nil, false
	}
	r := bytes.NewReader(attrBytes[12:])
	out := make(map[string][]byte, count)
	for i := 0; i < count; i++ {
		nameLen, err := readUint32(r)
		if err != nil || nameLen == 0 || nameLen > 255 || int(nameLen) > r.Len() {
			return nil, false
		}
		nameBytes := make([]byte, nameLen)
		if _, err := r.Read(nameBytes); err != nil {
			return nil, false
		}
		valLen, err := readUint32(r)
		if err != nil || valLen > 64<<10 || int(valLen) > r.Len() {
			return nil, false
		}
		valBytes := make([]byte, valLen)
		if _, err := r.Read(valBytes); err != nil {
			return nil, false
		}
		out[string(nameBytes)] = valBytes
	}
	return out, true
}

func syncAppleDoubleXattrsIfPresent(core *layeredfs.Core, qpath layeredfs.QualifiedPath) {
	targetRel, ok := isAppleDoublePath(qpath.Rel)
	if !ok {
		return
	}
	targetQPath := layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: targetRel}
	if _, err := core.Lstat(targetQPath); err != nil {
		return
	}
	data, err := core.ReadFile(qpath)
	if err != nil {
		return
	}
	_, xattrs, ok := DecodeAppleDoubleXattrs(data)
	if !ok {
		return
	}
	for k, v := range xattrs {
		_ = core.SetXattr(targetQPath, k, v, true)
	}
}

func flushChunksToCore(
	core *layeredfs.Core, handles *handleTable, fault *FaultInjector,
	rec *handleRecord, chunks []unstableChunk, snapshotJournal bool,
) (layeredfs.EntryAttr, error) {
	if rec.unlinked != nil {
		if rec.unlinked.fd == 0 {
			return layeredfs.EntryAttr{}, ErrStaleHandle
		}
		for _, ch := range chunks {
			if _, err := core.WriteHandle(rec.unlinked.fd, ch.data, int64(ch.offset)); err != nil {
				return layeredfs.EntryAttr{}, err
			}
		}
		if fault != nil && fault.OnSync != nil {
			if err := fault.OnSync(rec.viewID, rec.qpath); err != nil {
				return layeredfs.EntryAttr{}, err
			}
		}
		if err := core.SyncHandle(rec.unlinked.fd); err != nil {
			return layeredfs.EntryAttr{}, err
		}
		meta, err := core.StatHandle(rec.unlinked.fd)
		if err == nil {
			handles.UpdateUnlinkedMeta(rec.key, meta)
		}
		return meta, err
	}

	curMeta, err := core.Lstat(rec.qpath)
	if err != nil {
		return layeredfs.EntryAttr{}, err
	}
	if curMeta.Mode&0o200 == 0 && !rec.ownerCreatedRO {
		return layeredfs.EntryAttr{}, syscall.EACCES
	}
	origPerm := curMeta.Mode & os.ModePerm
	restoreRO := curMeta.Mode&0o200 == 0 && rec.ownerCreatedRO
	if restoreRO {
		if _, err := core.Chmod(rec.qpath, origPerm|0o200); err != nil {
			return layeredfs.EntryAttr{}, err
		}
		defer func() {
			if restoreRO {
				_, _ = core.Chmod(rec.qpath, origPerm)
			}
		}()
	}

	fd, _, err := core.Open(rec.qpath, layeredfs.OpenReadWrite, 0)
	if err != nil {
		return layeredfs.EntryAttr{}, err
	}
	for _, ch := range chunks {
		if _, err := core.WriteHandle(fd, ch.data, int64(ch.offset)); err != nil {
			_ = core.CloseHandle(fd)
			return layeredfs.EntryAttr{}, err
		}
	}
	if fault != nil && fault.OnSync != nil {
		if err := fault.OnSync(rec.viewID, rec.qpath); err != nil {
			_ = core.CloseHandle(fd)
			return layeredfs.EntryAttr{}, err
		}
	}
	if err := core.SyncHandle(fd); err != nil {
		_ = core.CloseHandle(fd)
		return layeredfs.EntryAttr{}, err
	}
	_ = core.CloseHandle(fd)
	if restoreRO {
		_, _ = core.Chmod(rec.qpath, origPerm)
		restoreRO = false
	}
	syncAppleDoubleXattrsIfPresent(core, rec.qpath)
	if snapshotJournal {
		if _, err := core.SnapshotJournal(); err != nil {
			return layeredfs.EntryAttr{}, err
		}
	}
	return core.Lstat(rec.qpath)
}
