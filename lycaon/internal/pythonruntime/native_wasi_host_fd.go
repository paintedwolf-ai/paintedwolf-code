package pythonruntime

import (
	"encoding/binary"
	"io"
	"io/fs"

	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/sys"
)
func (h *nativeWASIHost) getFD(fd uint32) (*nativeFD, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.fds[fd]
	return e, ok
}

func (h *nativeWASIHost) handleFdPrestatGet(mem []byte, args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	entry, ok := h.getFD(uint32(args[0]))
	if !ok || !entry.isPreopen {
		return wasiErrnoBadf
	}
	buf, ok := memSlice(mem, uint32(args[1]), 8)
	if !ok {
		return wasiErrnoFault
	}
	buf[0] = 0 // preopentype_dir
	buf[1], buf[2], buf[3] = 0, 0, 0
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(entry.mountName)))
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdPrestatDirName(mem []byte, args []uint64) uint32 {
	if len(args) < 3 {
		return wasiErrnoInval
	}
	entry, ok := h.getFD(uint32(args[0]))
	if !ok || !entry.isPreopen {
		return wasiErrnoBadf
	}
	dst, ok := memSlice(mem, uint32(args[1]), uint32(args[2]))
	if !ok {
		return wasiErrnoFault
	}
	copy(dst, entry.mountName)
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdFdstatGet(mem []byte, args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	fd := uint32(args[0])
	buf, ok := memSlice(mem, uint32(args[1]), 24)
	if !ok {
		return wasiErrnoFault
	}
	clear(buf)
	if fd <= 2 {
		buf[0] = wasiFiletypeCharacterDevice
		binary.LittleEndian.PutUint64(buf[8:16], ^uint64(0))
		binary.LittleEndian.PutUint64(buf[16:24], ^uint64(0))
		return wasiErrnoSuccess
	}
	entry, ok := h.getFD(fd)
	if !ok {
		return wasiErrnoBadf
	}
	if entry.isDir {
		buf[0] = wasiFiletypeDirectory
	} else {
		buf[0] = wasiFiletypeRegularFile
	}
	binary.LittleEndian.PutUint16(buf[2:4], entry.fdflags)
	binary.LittleEndian.PutUint64(buf[8:16], ^uint64(0))
	binary.LittleEndian.PutUint64(buf[16:24], ^uint64(0))
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdFdstatSetFlags(args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	entry, ok := h.getFD(uint32(args[0]))
	if !ok {
		return wasiErrnoBadf
	}
	entry.fdflags = uint16(args[1])
	return wasiErrnoSuccess
}

func writeFilestat(mem []byte, ptr uint32, st sys.Stat_t) bool {
	buf, ok := memSlice(mem, ptr, 64)
	if !ok {
		return false
	}
	clear(buf)
	binary.LittleEndian.PutUint64(buf[0:8], st.Dev)
	binary.LittleEndian.PutUint64(buf[8:16], st.Ino)
	ft := wasiFiletypeRegularFile
	switch {
	case st.Mode&fs.ModeDir != 0:
		ft = wasiFiletypeDirectory
	case st.Mode&fs.ModeSymlink != 0:
		ft = wasiFiletypeSymbolicLink
	case st.Mode&fs.ModeCharDevice != 0:
		ft = wasiFiletypeCharacterDevice
	}
	buf[16] = ft
	nlink := st.Nlink
	if nlink == 0 {
		nlink = 1
	}
	binary.LittleEndian.PutUint64(buf[24:32], nlink)
	binary.LittleEndian.PutUint64(buf[32:40], uint64(st.Size))
	binary.LittleEndian.PutUint64(buf[40:48], uint64(st.Atim))
	binary.LittleEndian.PutUint64(buf[48:56], uint64(st.Mtim))
	binary.LittleEndian.PutUint64(buf[56:64], uint64(st.Ctim))
	return true
}

func (h *nativeWASIHost) handleFdFilestatGet(mem []byte, args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	fd := uint32(args[0])
	ptr := uint32(args[1])
	if fd <= 2 {
		st := sys.Stat_t{Mode: fs.ModeCharDevice | 0o666, Nlink: 1}
		if !writeFilestat(mem, ptr, st) {
			return wasiErrnoFault
		}
		return wasiErrnoSuccess
	}
	entry, ok := h.getFD(fd)
	if !ok {
		return wasiErrnoBadf
	}
	var (
		st    sys.Stat_t
		errno experimentalsys.Errno
	)
	if entry.file != nil {
		st, errno = entry.file.Stat()
	} else {
		st, errno = entry.fs.Stat(entry.relPath)
	}
	if errno != 0 {
		return sysErrnoToWASI(errno)
	}
	if !writeFilestat(mem, ptr, st) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdFilestatSetSize(args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	entry, ok := h.getFD(uint32(args[0]))
	if !ok || entry.file == nil {
		return wasiErrnoBadf
	}
	return sysErrnoToWASI(entry.file.Truncate(int64(args[1])))
}

func (h *nativeWASIHost) handleFdFilestatSetTimes(args []uint64) uint32 {
	if len(args) < 4 {
		return wasiErrnoInval
	}
	entry, ok := h.getFD(uint32(args[0]))
	if !ok || entry.file == nil {
		return wasiErrnoBadf
	}
	atim, mtim := resolveWasiTimes(h.clock, int64(args[1]), int64(args[2]), uint32(args[3]))
	return sysErrnoToWASI(entry.file.Utimens(atim, mtim))
}

func (h *nativeWASIHost) handleFdClose(args []uint64) uint32 {
	if len(args) < 1 {
		return wasiErrnoInval
	}
	fd := uint32(args[0])
	if fd <= 2 {
		return wasiErrnoSuccess
	}
	h.mu.Lock()
	entry, ok := h.fds[fd]
	if ok && !entry.isPreopen {
		delete(h.fds, fd)
	}
	h.mu.Unlock()
	if !ok {
		return wasiErrnoBadf
	}
	if entry.isPreopen {
		return wasiErrnoSuccess
	}
	if entry.file != nil {
		return sysErrnoToWASI(entry.file.Close())
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdSync(args []uint64, dataOnly bool) uint32 {
	if len(args) < 1 {
		return wasiErrnoInval
	}
	entry, ok := h.getFD(uint32(args[0]))
	if !ok || entry.file == nil {
		return wasiErrnoBadf
	}
	if dataOnly {
		return sysErrnoToWASI(entry.file.Datasync())
	}
	return sysErrnoToWASI(entry.file.Sync())
}

func (h *nativeWASIHost) handleFdRead(mem []byte, args []uint64) uint32 {
	if len(args) < 4 {
		return wasiErrnoInval
	}
	fd := uint32(args[0])
	iovsPtr := uint32(args[1])
	iovsLen := uint32(args[2])
	nreadPtr := uint32(args[3])

	iovs, ok := memSlice(mem, iovsPtr, iovsLen*8)
	if !ok {
		return wasiErrnoFault
	}
	var total uint32
	for i := uint32(0); i < iovsLen; i++ {
		bufPtr := binary.LittleEndian.Uint32(iovs[i*8 : i*8+4])
		bufLen := binary.LittleEndian.Uint32(iovs[i*8+4 : i*8+8])
		if bufLen == 0 {
			continue
		}
		dst, ok := memSlice(mem, bufPtr, bufLen)
		if !ok {
			return wasiErrnoFault
		}
		if fd == 0 {
			n, err := h.stdin.Read(dst)
			total += uint32(n)
			if err != nil && err != io.EOF {
				return wasiErrnoIO
			}
			if n < int(bufLen) || err == io.EOF {
				break
			}
			continue
		}
		entry, ok := h.getFD(fd)
		if !ok || entry.file == nil {
			return wasiErrnoBadf
		}
		n, errno := entry.file.Read(dst)
		total += uint32(n)
		if errno != 0 {
			return sysErrnoToWASI(errno)
		}
		if n < int(bufLen) {
			break
		}
	}
	if !putU32(mem, nreadPtr, total) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdPread(mem []byte, args []uint64) uint32 {
	if len(args) < 5 {
		return wasiErrnoInval
	}
	entry, ok := h.getFD(uint32(args[0]))
	if !ok || entry.file == nil {
		return wasiErrnoBadf
	}
	iovsPtr := uint32(args[1])
	iovsLen := uint32(args[2])
	offset := int64(args[3])
	nreadPtr := uint32(args[4])

	iovs, ok := memSlice(mem, iovsPtr, iovsLen*8)
	if !ok {
		return wasiErrnoFault
	}
	var total uint32
	for i := uint32(0); i < iovsLen; i++ {
		bufPtr := binary.LittleEndian.Uint32(iovs[i*8 : i*8+4])
		bufLen := binary.LittleEndian.Uint32(iovs[i*8+4 : i*8+8])
		if bufLen == 0 {
			continue
		}
		dst, ok := memSlice(mem, bufPtr, bufLen)
		if !ok {
			return wasiErrnoFault
		}
		n, errno := entry.file.Pread(dst, offset+int64(total))
		total += uint32(n)
		if errno != 0 {
			return sysErrnoToWASI(errno)
		}
		if n < int(bufLen) {
			break
		}
	}
	if !putU32(mem, nreadPtr, total) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdWrite(mem []byte, args []uint64) uint32 {
	if len(args) < 4 {
		return wasiErrnoInval
	}
	fd := uint32(args[0])
	iovsPtr := uint32(args[1])
	iovsLen := uint32(args[2])
	nwrittenPtr := uint32(args[3])

	iovs, ok := memSlice(mem, iovsPtr, iovsLen*8)
	if !ok {
		return wasiErrnoFault
	}
	var total uint32
	for i := uint32(0); i < iovsLen; i++ {
		bufPtr := binary.LittleEndian.Uint32(iovs[i*8 : i*8+4])
		bufLen := binary.LittleEndian.Uint32(iovs[i*8+4 : i*8+8])
		if bufLen == 0 {
			continue
		}
		src, ok := memSlice(mem, bufPtr, bufLen)
		if !ok {
			return wasiErrnoFault
		}
		if fd == 1 || fd == 2 {
			w := h.stdout
			if fd == 2 {
				w = h.stderr
			}
			n, err := w.Write(src)
			total += uint32(n)
			if err != nil {
				return wasiErrnoIO
			}
			continue
		}
		entry, ok := h.getFD(fd)
		if !ok || entry.file == nil {
			return wasiErrnoBadf
		}
		n, errno := entry.file.Write(src)
		total += uint32(n)
		if errno != 0 {
			return sysErrnoToWASI(errno)
		}
	}
	if !putU32(mem, nwrittenPtr, total) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdPwrite(mem []byte, args []uint64) uint32 {
	if len(args) < 5 {
		return wasiErrnoInval
	}
	entry, ok := h.getFD(uint32(args[0]))
	if !ok || entry.file == nil {
		return wasiErrnoBadf
	}
	iovsPtr := uint32(args[1])
	iovsLen := uint32(args[2])
	offset := int64(args[3])
	nwrittenPtr := uint32(args[4])

	iovs, ok := memSlice(mem, iovsPtr, iovsLen*8)
	if !ok {
		return wasiErrnoFault
	}
	var total uint32
	for i := uint32(0); i < iovsLen; i++ {
		bufPtr := binary.LittleEndian.Uint32(iovs[i*8 : i*8+4])
		bufLen := binary.LittleEndian.Uint32(iovs[i*8+4 : i*8+8])
		if bufLen == 0 {
			continue
		}
		src, ok := memSlice(mem, bufPtr, bufLen)
		if !ok {
			return wasiErrnoFault
		}
		n, errno := entry.file.Pwrite(src, offset+int64(total))
		total += uint32(n)
		if errno != 0 {
			return sysErrnoToWASI(errno)
		}
	}
	if !putU32(mem, nwrittenPtr, total) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdSeek(mem []byte, args []uint64) uint32 {
	if len(args) < 4 {
		return wasiErrnoInval
	}
	fd := uint32(args[0])
	if fd <= 2 {
		return wasiErrnoSpipe
	}
	entry, ok := h.getFD(fd)
	if !ok || entry.file == nil {
		return wasiErrnoBadf
	}
	pos, errno := entry.file.Seek(int64(args[1]), int(uint32(args[2])))
	if errno != 0 {
		return sysErrnoToWASI(errno)
	}
	if !putU64(mem, uint32(args[3]), uint64(pos)) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdTell(mem []byte, args []uint64) uint32 {
	if len(args) < 2 {
		return wasiErrnoInval
	}
	fd := uint32(args[0])
	if fd <= 2 {
		return wasiErrnoSpipe
	}
	entry, ok := h.getFD(fd)
	if !ok || entry.file == nil {
		return wasiErrnoBadf
	}
	pos, errno := entry.file.Seek(0, io.SeekCurrent)
	if errno != 0 {
		return sysErrnoToWASI(errno)
	}
	if !putU64(mem, uint32(args[1]), uint64(pos)) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

func (h *nativeWASIHost) handleFdReaddir(mem []byte, args []uint64) uint32 {
	if len(args) < 5 {
		return wasiErrnoInval
	}
	entry, ok := h.getFD(uint32(args[0]))
	if !ok || !entry.isDir {
		return wasiErrnoBadf
	}
	bufPtr := uint32(args[1])
	bufLen := uint32(args[2])
	cookie := args[3]
	usedPtr := uint32(args[4])

	buf, ok := memSlice(mem, bufPtr, bufLen)
	if !ok {
		return wasiErrnoFault
	}
	if cookie == 0 || !entry.dirLoaded {
		var f experimentalsys.File
		var needClose bool
		if entry.file != nil {
			f = entry.file
		} else {
			opened, errno := entry.fs.OpenFile(entry.relPath, experimentalsys.O_RDONLY|experimentalsys.O_DIRECTORY, 0)
			if errno != 0 {
				return sysErrnoToWASI(errno)
			}
			f = opened
			needClose = true
		}
		entries, errno := f.Readdir(-1)
		if needClose {
			_ = f.Close()
		}
		if errno != 0 {
			return sysErrnoToWASI(errno)
		}
		entry.dirEntries = entries
		entry.dirLoaded = true
	}

	var used uint32
	for idx := int(cookie); idx < len(entry.dirEntries); idx++ {
		de := entry.dirEntries[idx]
		nameBytes := []byte(de.Name)
		recLen := uint32(24 + len(nameBytes))
		var hdr [24]byte
		binary.LittleEndian.PutUint64(hdr[0:8], uint64(idx+1))
		binary.LittleEndian.PutUint64(hdr[8:16], de.Ino)
		binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(nameBytes)))
		ft := wasiFiletypeRegularFile
		if de.Type&fs.ModeDir != 0 {
			ft = wasiFiletypeDirectory
		} else if de.Type&fs.ModeSymlink != 0 {
			ft = wasiFiletypeSymbolicLink
		}
		hdr[20] = ft

		if used+24 > bufLen {
			copy(buf[used:], hdr[:bufLen-used])
			used = bufLen
			break
		}
		copy(buf[used:used+24], hdr[:])
		used += 24
		if used+uint32(len(nameBytes)) > bufLen {
			copy(buf[used:], nameBytes[:bufLen-used])
			used = bufLen
			break
		}
		copy(buf[used:used+uint32(len(nameBytes))], nameBytes)
		used += uint32(len(nameBytes))
		_ = recLen
	}
	if !putU32(mem, usedPtr, used) {
		return wasiErrnoFault
	}
	return wasiErrnoSuccess
}

