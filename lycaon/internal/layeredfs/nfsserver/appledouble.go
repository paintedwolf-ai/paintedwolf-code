package nfsserver

import (
	"bytes"
	"encoding/binary"
	"path"
	"sort"
	"strings"

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

// The layout below is the one the macOS kernel reads and writes for ._ files (xnu vfs_xattr.c):
// a two-entry AppleDouble header, Finder info, then an ATTR table inside the Finder info entry.
const (
	appleDoubleResource   uint32 = 2
	appleDoubleFinderOff         = 50
	appleDoubleAttrHdrOff        = 84
	appleDoubleEntriesOff        = 120
	appleDoubleMinSize           = 4096
	appleDoubleFiller            = "Mac OS X        "
	finderInfoXattr              = "com.apple.FinderInfo"
	resourceForkXattr            = "com.apple.ResourceFork"
)

// emptyResourceFork is the placeholder fork the kernel writes into a new ._ file. It is the file's
// layout, not an attribute: the kernel does not report it through getxattr.
var emptyResourceFork = func() []byte {
	fork := make([]byte, 286)
	for _, at := range []int{0, 256} {
		copy(fork[at:], []byte{0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0x1e})
	}
	copy(fork[16:], "This resource fork intentionally left blank   ")
	copy(fork[280:], []byte{0, 0x1c, 0, 0x1e, 0xff, 0xff})
	return fork
}()

// EncodeAppleDoubleXattrs encodes Finder info and extended attributes as the kernel's ._ file format.
func EncodeAppleDoubleXattrs(finderInfo [32]byte, xattrs map[string][]byte) []byte {
	if fi, ok := xattrs[finderInfoXattr]; ok && len(fi) == 32 {
		copy(finderInfo[:], fi)
	}
	names := make([]string, 0, len(xattrs))
	for k := range xattrs {
		if k != finderInfoXattr && k != resourceForkXattr {
			names = append(names, k)
		}
	}
	sort.Strings(names)

	entriesLen := 0
	for _, name := range names {
		entriesLen += (11 + len(name) + 1 + 3) &^ 3
	}
	dataStart := appleDoubleEntriesOff + entriesLen
	var data bytes.Buffer
	var entries bytes.Buffer
	for _, name := range names {
		val := xattrs[name]
		start := entries.Len()
		writeUint32(&entries, uint32(dataStart+data.Len()))
		writeUint32(&entries, uint32(len(val)))
		_, _ = entries.Write([]byte{0, 0, byte(len(name) + 1)})
		_, _ = entries.WriteString(name)
		_ = entries.WriteByte(0)
		for (entries.Len()-start)%4 != 0 {
			_ = entries.WriteByte(0)
		}
		_, _ = data.Write(val)
	}
	end := max(dataStart+data.Len(), appleDoubleMinSize)
	rsrc := xattrs[resourceForkXattr]

	var out bytes.Buffer
	out.Grow(end + len(rsrc))
	writeUint32(&out, appleDoubleMagic)
	writeUint32(&out, appleDoubleVersion)
	_, _ = out.WriteString(appleDoubleFiller)
	_, _ = out.Write([]byte{0, 2})
	for _, v := range []uint32{appleDoubleFinder, appleDoubleFinderOff, uint32(end - appleDoubleFinderOff), appleDoubleResource, uint32(end), uint32(len(rsrc))} {
		writeUint32(&out, v)
	}
	_, _ = out.Write(finderInfo[:])
	_, _ = out.Write([]byte{0, 0})
	for _, v := range []uint32{appleDoubleAttrMag, 0, uint32(end), uint32(dataStart), uint32(data.Len()), 0, 0, 0} {
		writeUint32(&out, v)
	}
	_, _ = out.Write([]byte{0, 0, byte(len(names) >> 8), byte(len(names))})
	_, _ = out.Write(entries.Bytes())
	_, _ = out.Write(data.Bytes())
	out.Write(make([]byte, end-out.Len()))
	_, _ = out.Write(rsrc)
	return out.Bytes()
}

// DecodeAppleDoubleXattrs parses a ._ file in the kernel's layout. It succeeds only if the header,
// table and values are consistent and no field that should be empty holds anything, so a success means
// the returned attributes are everything a reader of the format can get from the file. The area past
// the header's data length is free space the kernel does not clear when an attribute shrinks. Non-zero
// Finder info and a resource fork are reported under their attribute names, as the view stores them.
func DecodeAppleDoubleXattrs(data []byte) ([32]byte, map[string][]byte, bool) {
	var finderInfo [32]byte
	if len(data) < appleDoubleEntriesOff || binary.BigEndian.Uint32(data[0:4]) != appleDoubleMagic ||
		binary.BigEndian.Uint32(data[4:8]) != appleDoubleVersion || string(data[8:24]) != appleDoubleFiller ||
		binary.BigEndian.Uint16(data[24:26]) != 2 {
		return finderInfo, nil, false
	}
	entry := func(i int) (id, off, length uint32) {
		at := 26 + i*12
		return binary.BigEndian.Uint32(data[at:]), binary.BigEndian.Uint32(data[at+4:]), binary.BigEndian.Uint32(data[at+8:])
	}
	finderID, finderOff, finderLen := entry(0)
	rsrcID, rsrcOff, rsrcLen := entry(1)
	attrsEnd := uint64(finderOff) + uint64(finderLen)
	if finderID != appleDoubleFinder || finderOff != appleDoubleFinderOff || rsrcID != appleDoubleResource ||
		attrsEnd != uint64(rsrcOff) || attrsEnd < appleDoubleEntriesOff ||
		attrsEnd+uint64(rsrcLen) != uint64(len(data)) ||
		binary.BigEndian.Uint32(data[appleDoubleAttrHdrOff:]) != appleDoubleAttrMag {
		return finderInfo, nil, false
	}
	copy(finderInfo[:], data[appleDoubleFinderOff:])
	dataStart := uint64(binary.BigEndian.Uint32(data[appleDoubleAttrHdrOff+12:]))
	dataEnd := dataStart + uint64(binary.BigEndian.Uint32(data[appleDoubleAttrHdrOff+16:]))
	if dataEnd > attrsEnd {
		return finderInfo, nil, false
	}
	out, entriesEnd, ok := parseAppleDoubleAttrs(data, dataStart, dataEnd)
	if !ok {
		return finderInfo, nil, false
	}
	if finderInfo != ([32]byte{}) {
		out[finderInfoXattr] = append([]byte(nil), finderInfo[:]...)
	}
	if rsrcLen > 0 && !bytes.Equal(data[attrsEnd:], emptyResourceFork) {
		out[resourceForkXattr] = append([]byte(nil), data[attrsEnd:]...)
	}
	if !allZero(data[entriesEnd:dataStart]) || !allZero(data[appleDoubleFinderOff+32:appleDoubleAttrHdrOff]) ||
		!allZero(data[appleDoubleAttrHdrOff+20:appleDoubleEntriesOff-2]) {
		return finderInfo, nil, false
	}
	return finderInfo, out, true
}

// parseAppleDoubleAttrs walks the attribute table, which ends at or before dataStart, and returns the
// attributes and where the table ends. Every value lies inside the data area.
func parseAppleDoubleAttrs(data []byte, dataStart, dataEnd uint64) (map[string][]byte, uint64, bool) {
	count := int(binary.BigEndian.Uint16(data[appleDoubleEntriesOff-2:]))
	out := make(map[string][]byte, count)
	cursor := uint64(appleDoubleEntriesOff)
	for range count {
		if cursor+11 > dataStart {
			return nil, 0, false
		}
		valOff := uint64(binary.BigEndian.Uint32(data[cursor:]))
		valLen := uint64(binary.BigEndian.Uint32(data[cursor+4:]))
		flags := binary.BigEndian.Uint16(data[cursor+8:])
		nameLen := uint64(data[cursor+10])
		nameEnd := cursor + 11 + nameLen
		if flags != 0 || nameLen < 2 || nameEnd > dataStart || data[nameEnd-1] != 0 {
			return nil, 0, false
		}
		name := string(data[cursor+11 : nameEnd-1])
		_, duplicate := out[name]
		if duplicate || strings.IndexByte(name, 0) >= 0 || name == finderInfoXattr || name == resourceForkXattr ||
			valOff < dataStart || valOff+valLen > dataEnd {
			return nil, 0, false
		}
		out[name] = append([]byte(nil), data[valOff:valOff+valLen]...)
		cursor = (nameEnd + 3) &^ 3
	}
	return out, cursor, cursor <= dataStart
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

func flushChunksToCore(
	core *layeredfs.Core, handles *handleTable, sidecars *sidecarTable, fault *FaultInjector,
	rec *handleRecord, chunks []unstableChunk,
) (layeredfs.EntryAttr, error) {
	if rec.unlinked != nil {
		if rec.unlinked.fd == 0 {
			return layeredfs.EntryAttr{}, ErrStaleHandle
		}
		if err := writeAndSyncChunks(core, fault, rec, rec.unlinked.fd, chunks); err != nil {
			return layeredfs.EntryAttr{}, err
		}
		meta, err := core.StatHandle(rec.unlinked.fd)
		if err == nil {
			handles.UpdateUnlinkedMeta(rec.key, meta)
		}
		return meta, err
	}
	if err := sidecars.materialize(core, rec.qpath); err != nil {
		return layeredfs.EntryAttr{}, err
	}
	fd, opened, err := core.Open(rec.qpath, layeredfs.OpenReadWrite, 0)
	if err != nil {
		return layeredfs.EntryAttr{}, err
	}
	if opened.StableID() != rec.key.nodeID {
		_ = core.CloseHandle(fd)
		return layeredfs.EntryAttr{}, ErrStaleHandle
	}
	err = writeAndSyncChunks(core, fault, rec, fd, chunks)
	_ = core.CloseHandle(fd)
	if err != nil {
		return layeredfs.EntryAttr{}, err
	}
	return core.Lstat(rec.qpath)
}

func writeAndSyncChunks(core *layeredfs.Core, fault *FaultInjector, rec *handleRecord, fd layeredfs.HandleID, chunks []unstableChunk) error {
	for _, ch := range chunks {
		if _, err := core.WriteHandle(fd, ch.data, int64(ch.offset)); err != nil {
			return err
		}
	}
	if fault != nil && fault.OnSync != nil {
		if err := fault.OnSync(rec.viewID, rec.qpath); err != nil {
			return err
		}
	}
	return core.SyncHandle(fd)
}
