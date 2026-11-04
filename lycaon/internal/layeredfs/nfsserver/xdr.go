package nfsserver

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	nfs "github.com/willscott/go-nfs"
)

// sattr3 represents RFC 1813 section 2.5.4 settable file attributes with explicit presence flags.
type sattr3 struct {
	HasMode            bool
	Mode               os.FileMode
	HasUID, HasGID     bool
	UID, GID           uint32
	HasSize            bool
	Size               uint64
	HasAtime, HasMtime bool
	Atime, Mtime       time.Time
}

const (
	timeDontChange      = 0
	timeSetToServerTime = 1
	timeSetToClientTime = 2
)

func readUint32(r *bytes.Reader) (uint32, error) {
	if r.Len() < 4 {
		return 0, io.ErrUnexpectedEOF
	}
	var buf [4]byte
	_, err := io.ReadFull(r, buf[:])
	return binary.BigEndian.Uint32(buf[:]), err
}

func readUint64(r *bytes.Reader) (uint64, error) {
	if r.Len() < 8 {
		return 0, io.ErrUnexpectedEOF
	}
	var buf [8]byte
	_, err := io.ReadFull(r, buf[:])
	return binary.BigEndian.Uint64(buf[:]), err
}

func readBool(r *bytes.Reader) (bool, error) {
	v, err := readUint32(r)
	if err != nil {
		return false, err
	}
	if v > 1 {
		return false, fmt.Errorf("invalid XDR bool: %d", v)
	}
	return v == 1, nil
}

// readOpaqueBounded reads a length-prefixed XDR opaque slice only after verifying
// both maxLen and remaining bytes in the reader, preventing oversized allocations.
func readOpaqueBounded(r *bytes.Reader, maxLen int) ([]byte, error) {
	length, err := readUint32(r)
	if err != nil {
		return nil, err
	}
	if maxLen >= 0 && uint64(length) > uint64(maxLen) {
		return nil, fmt.Errorf("XDR opaque length %d exceeds max %d", length, maxLen)
	}
	pad := (4 - (length & 3)) & 3
	total := uint64(length) + uint64(pad)
	if total > uint64(r.Len()) {
		return nil, io.ErrUnexpectedEOF
	}
	out := make([]byte, length)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	if pad > 0 {
		var discard [4]byte
		if _, err := io.ReadFull(r, discard[:pad]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func readStringBounded(r *bytes.Reader, maxLen int) (string, error) {
	b, err := readOpaqueBounded(r, maxLen)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func readFileHandle(r *bytes.Reader) ([]byte, error) {
	return readOpaqueBounded(r, nfs.FHSize)
}

func readDirOpArg(r *bytes.Reader) ([]byte, string, error) {
	fh, err := readFileHandle(r)
	if err != nil {
		return nil, "", err
	}
	name, err := readStringBounded(r, nfs.MNTNameLen)
	if err != nil {
		return nil, "", err
	}
	return fh, name, nil
}

func readFixedBytes(r *bytes.Reader, n int) ([]byte, error) {
	if r.Len() < n {
		return nil, io.ErrUnexpectedEOF
	}
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

func readSattr3(r *bytes.Reader, now time.Time) (sattr3, error) {
	var out sattr3
	var err error
	if out.HasMode, err = readBool(r); err != nil {
		return out, err
	}
	if out.HasMode {
		m, err := readUint32(r)
		if err != nil {
			return out, err
		}
		out.Mode = os.FileMode(m) & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	}
	if out.HasUID, err = readBool(r); err != nil {
		return out, err
	}
	if out.HasUID {
		if out.UID, err = readUint32(r); err != nil {
			return out, err
		}
	}
	if out.HasGID, err = readBool(r); err != nil {
		return out, err
	}
	if out.HasGID {
		if out.GID, err = readUint32(r); err != nil {
			return out, err
		}
	}
	if out.HasSize, err = readBool(r); err != nil {
		return out, err
	}
	if out.HasSize {
		if out.Size, err = readUint64(r); err != nil {
			return out, err
		}
	}
	if out.HasAtime, out.Atime, err = readSetTime(r, now); err != nil {
		return out, err
	}
	if out.HasMtime, out.Mtime, err = readSetTime(r, now); err != nil {
		return out, err
	}
	return out, nil
}

func readSetTime(r *bytes.Reader, now time.Time) (bool, time.Time, error) {
	how, err := readUint32(r)
	if err != nil {
		return false, time.Time{}, err
	}
	switch how {
	case timeDontChange:
		return false, time.Time{}, nil
	case timeSetToServerTime:
		return true, now, nil
	case timeSetToClientTime:
		sec, err := readUint32(r)
		if err != nil {
			return false, time.Time{}, err
		}
		nsec, err := readUint32(r)
		if err != nil {
			return false, time.Time{}, err
		}
		return true, time.Unix(int64(sec), int64(nsec)), nil
	default:
		return false, time.Time{}, fmt.Errorf("invalid set_time how: %d", how)
	}
}

func writeUint32(w *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	_, _ = w.Write(b[:])
}

func writeUint64(w *bytes.Buffer, v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	_, _ = w.Write(b[:])
}

func writeBool(w *bytes.Buffer, v bool) {
	if v {
		writeUint32(w, 1)
	} else {
		writeUint32(w, 0)
	}
}

func writeOpaque(w *bytes.Buffer, data []byte) {
	writeUint32(w, uint32(len(data)))
	_, _ = w.Write(data)
	pad := (4 - (len(data) & 3)) & 3
	if pad > 0 {
		var zeroes [4]byte
		_, _ = w.Write(zeroes[:pad])
	}
}

func writeString(w *bytes.Buffer, s string) { writeOpaque(w, []byte(s)) }

func writePostOpFH3(w *bytes.Buffer, fh []byte) {
	if len(fh) == 0 {
		writeUint32(w, 0)
		return
	}
	writeUint32(w, 1)
	writeOpaque(w, fh)
}

func nodeMetaToFileAttr(meta *layeredfs.EntryAttr, uid, gid uint32) *nfs.FileAttribute {
	if meta == nil {
		return nil
	}
	ft := nfs.FileTypeRegular
	switch {
	case meta.Kind == layeredfs.EntryDirectory || meta.Mode.IsDir():
		ft = nfs.FileTypeDirectory
	case meta.Kind == layeredfs.EntrySymlink || meta.Mode&os.ModeSymlink != 0:
		ft = nfs.FileTypeLink
	case meta.Kind == layeredfs.EntryFIFO:
		ft = nfs.FileTypeFIFO
	case meta.Kind == layeredfs.EntrySocket:
		ft = nfs.FileTypeSocket
	}
	nlink := meta.Nlink
	if nlink == 0 {
		nlink = 1
		if ft == nfs.FileTypeDirectory {
			nlink = 2
		}
	}
	size := uint64(0)
	if meta.Size > 0 {
		size = uint64(meta.Size)
	}
	atime, mtime, ctime := meta.ATime, meta.ModTime, meta.CTime
	if atime.IsZero() {
		atime = mtime
	}
	if ctime.IsZero() {
		ctime = mtime
	}
	return &nfs.FileAttribute{
		Type: ft, FileMode: uint32(meta.Mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)),
		Nlink: nlink, UID: uid, GID: gid, Filesize: size, Used: (size + 4095) &^ 4095,
		FSID: 1, Fileid: uint64(meta.NodeID),
		Atime: nfs.ToNFSTime(atime), Mtime: nfs.ToNFSTime(mtime), Ctime: nfs.ToNFSTime(ctime),
	}
}

func nodeMetaToCacheAttr(meta *layeredfs.EntryAttr) *nfs.FileCacheAttribute {
	if meta == nil {
		return nil
	}
	size := uint64(0)
	if meta.Size > 0 {
		size = uint64(meta.Size)
	}
	ctime := meta.CTime
	if ctime.IsZero() {
		ctime = meta.ModTime
	}
	return &nfs.FileCacheAttribute{
		Filesize: size,
		Mtime:    nfs.ToNFSTime(meta.ModTime),
		Ctime:    nfs.ToNFSTime(ctime),
	}
}

func writePostOpAttr(w *bytes.Buffer, meta *layeredfs.EntryAttr, uid, gid uint32) {
	_ = nfs.WritePostOpAttrs(w, nodeMetaToFileAttr(meta, uid, gid))
}

func writeWccData(w *bytes.Buffer, pre *layeredfs.EntryAttr, post *layeredfs.EntryAttr, uid, gid uint32) {
	_ = nfs.WriteWcc(w, nodeMetaToCacheAttr(pre), nodeMetaToFileAttr(post, uid, gid))
}
