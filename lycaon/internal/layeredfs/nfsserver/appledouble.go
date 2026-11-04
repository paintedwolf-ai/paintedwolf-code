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
	finderInfoXattr              = "com.apple.FinderInfo"
	resourceForkXattr            = "com.apple.ResourceFork"
)

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
	_, _ = out.WriteString("Mac OS X        ")
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

// DecodeAppleDoubleXattrs parses a ._ file. Non-zero Finder info is also reported under its xattr
// name, since that is how the rest of the view stores it.
func DecodeAppleDoubleXattrs(data []byte) ([32]byte, map[string][]byte, bool) {
	var finderInfo [32]byte
	if len(data) < 26 || binary.BigEndian.Uint32(data[0:4]) != appleDoubleMagic ||
		binary.BigEndian.Uint32(data[4:8]) != appleDoubleVersion {
		return finderInfo, nil, false
	}
	numEntries := int(binary.BigEndian.Uint16(data[24:26]))
	if numEntries <= 0 || numEntries > 32 || len(data) < 26+numEntries*12 {
		return finderInfo, nil, false
	}
	for i := range numEntries {
		off := 26 + i*12
		if binary.BigEndian.Uint32(data[off:off+4]) != appleDoubleFinder {
			continue
		}
		entryOff := uint64(binary.BigEndian.Uint32(data[off+4 : off+8]))
		entryLen := uint64(binary.BigEndian.Uint32(data[off+8 : off+12]))
		if entryOff+entryLen > uint64(len(data)) || entryLen < 32 {
			return finderInfo, nil, false
		}
		copy(finderInfo[:], data[entryOff:entryOff+32])
		out := parseAppleDoubleAttrs(data, entryOff, entryLen)
		if finderInfo != ([32]byte{}) {
			out[finderInfoXattr] = append([]byte(nil), finderInfo[:]...)
		}
		return finderInfo, out, true
	}
	return finderInfo, nil, false
}

func parseAppleDoubleAttrs(data []byte, finderOff, finderLen uint64) map[string][]byte {
	out := make(map[string][]byte)
	hdr := finderOff + 32 + 2
	if hdr+36 > finderOff+finderLen || binary.BigEndian.Uint32(data[hdr:hdr+4]) != appleDoubleAttrMag {
		return out
	}
	count := int(binary.BigEndian.Uint16(data[hdr+34 : hdr+36]))
	cursor := hdr + 36
	limit := finderOff + finderLen
	for range count {
		if cursor+11 > limit {
			return out
		}
		valOff := uint64(binary.BigEndian.Uint32(data[cursor : cursor+4]))
		valLen := uint64(binary.BigEndian.Uint32(data[cursor+4 : cursor+8]))
		nameLen := uint64(data[cursor+10])
		if nameLen == 0 || cursor+11+nameLen > limit || valOff+valLen > uint64(len(data)) {
			return out
		}
		name := strings.TrimRight(string(data[cursor+11:cursor+11+nameLen]), "\x00")
		if name != "" {
			out[name] = append([]byte(nil), data[valOff:valOff+valLen]...)
		}
		cursor = (cursor + 11 + nameLen + 3) &^ 3
	}
	return out
}

func synthAppleDoubleAttr(core *layeredfs.Core, qpath layeredfs.QualifiedPath) (layeredfs.EntryAttr, []byte, bool) {
	targetRel, ok := isAppleDoublePath(qpath.Rel)
	if !ok {
		return layeredfs.EntryAttr{}, nil, false
	}
	targetMeta, err := core.Lstat(layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: targetRel})
	if err != nil || len(targetMeta.Xattrs) == 0 {
		return layeredfs.EntryAttr{}, nil, false
	}
	payload := EncodeAppleDoubleXattrs([32]byte{}, targetMeta.Xattrs)
	adNodeID := layeredfs.NodeID(uint64(targetMeta.StableID()) ^ 0xAD00000000000001)
	if adNodeID == 0 {
		adNodeID = 1
	}
	return layeredfs.EntryAttr{
		NodeID: adNodeID, Generation: 1, Layer: targetMeta.Layer,
		Kind: layeredfs.EntryRegular, Mode: 0o644, Size: int64(len(payload)),
		ModTime: targetMeta.ModTime, ATime: targetMeta.ATime, CTime: targetMeta.CTime, Nlink: 1,
	}, payload, true
}

func syncAppleDoubleForPath(core *layeredfs.Core, qpath layeredfs.QualifiedPath) {
	if _, ok := isAppleDoublePath(qpath.Rel); ok {
		syncAppleDoubleXattrsIfPresent(core, qpath)
		return
	}
	dir, base := path.Dir(qpath.Rel), path.Base(qpath.Rel)
	sidecarRel := "._" + base
	if dir != "." && dir != "" {
		sidecarRel = dir + "/._" + base
	}
	syncAppleDoubleXattrsIfPresent(core, layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: sidecarRel})
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
	target, err := core.Lstat(targetQPath)
	if err != nil {
		return
	}
	for k, v := range xattrs {
		if !bytes.Equal(target.Xattrs[k], v) || target.Xattrs[k] == nil {
			_ = core.SetXattr(targetQPath, k, v, true)
		}
	}
	for k := range target.Xattrs {
		if _, kept := xattrs[k]; !kept {
			_ = core.RemoveXattr(targetQPath, k, true)
		}
	}
}

// materializeSidecar turns the ._ file synthesized from a file's stored xattrs into a real private
// file, keeping its identity, because the kernel is about to change it through a handle it holds.
func materializeSidecar(core *layeredfs.Core, qpath layeredfs.QualifiedPath) error {
	if _, ok := isAppleDoublePath(qpath.Rel); !ok {
		return nil
	}
	if _, err := core.Lstat(qpath); err == nil {
		return nil
	}
	meta, payload, ok := synthAppleDoubleAttr(core, qpath)
	if !ok {
		return nil
	}
	if _, err := core.WriteFile(qpath, payload, meta.Mode); err != nil {
		return err
	}
	core.AdoptIdentity(qpath, meta.NodeID)
	return nil
}

// removeSidecarXattrs keeps a removed ._ file from coming back: with no attributes left on the
// target there is nothing to synthesize it from.
func removeSidecarXattrs(core *layeredfs.Core, qpath layeredfs.QualifiedPath) {
	targetRel, ok := isAppleDoublePath(qpath.Rel)
	if !ok {
		return
	}
	targetQPath := layeredfs.QualifiedPath{RootID: qpath.RootID, Rel: targetRel}
	target, err := core.Lstat(targetQPath)
	if err != nil {
		return
	}
	for k := range target.Xattrs {
		_ = core.RemoveXattr(targetQPath, k, true)
	}
}

func flushChunksToCore(
	core *layeredfs.Core, handles *handleTable, fault *FaultInjector,
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
	syncAppleDoubleForPath(core, rec.qpath)
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
