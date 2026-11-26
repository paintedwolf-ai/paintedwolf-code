package pythondocuments

import (
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/pythonruntime"
)

// overlayFile implements pythonruntime.GuestFile over a byte-oriented
// overlayNode. Multiple open handles to the same path share the underlying
// overlayNode while maintaining independent offsets and close state.
type overlayFile struct {
	adapter          *OverlayAdapter
	node             *overlayNode
	flags            pythonruntime.OpenFlags
	readOpID         string
	parentOpID       string
	writeOriginFrame *pythonruntime.DeclaredFrame

	mu     sync.Mutex
	offset int64
	closed bool
}

var _ pythonruntime.GuestFile = (*overlayFile)(nil)

func (f *overlayFile) canRead() bool {
	if f.flags&pythonruntime.OpenRead != 0 {
		return true
	}
	// If neither OpenRead nor OpenWrite is set, default to readable.
	return f.flags&pythonruntime.OpenWrite == 0
}

func (f *overlayFile) canWrite() bool {
	return f.flags&(pythonruntime.OpenWrite|pythonruntime.OpenAppend) != 0
}

func (f *overlayFile) Read(p []byte) (int, pythonruntime.Errno) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || !f.canRead() {
		return 0, pythonruntime.ErrnoEBADF
	}
	f.node.mu.Lock()
	if f.node.kind == nodeDir {
		f.node.mu.Unlock()
		return 0, pythonruntime.ErrnoEISDIR
	}
	if f.offset >= int64(len(f.node.data)) {
		f.node.mu.Unlock()
		return 0, pythonruntime.ErrnoSuccess
	}
	start := f.offset
	n := copy(p, f.node.data[f.offset:])
	f.offset += int64(n)
	f.node.mu.Unlock()

	if n > 0 && f.readOpID != "" && f.adapter != nil && f.adapter.evidence != nil {
		f.adapter.evidence.RecordDocumentReadBytes(f.readOpID, ByteRange{
			Start: start,
			End:   start + int64(n),
		})
	}
	return n, pythonruntime.ErrnoSuccess
}

func (f *overlayFile) Write(p []byte) (int, pythonruntime.Errno) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || !f.canWrite() {
		return 0, pythonruntime.ErrnoEBADF
	}
	f.node.mu.Lock()
	if f.node.kind == nodeDir {
		f.node.mu.Unlock()
		return 0, pythonruntime.ErrnoEISDIR
	}
	if f.flags&pythonruntime.OpenAppend != 0 {
		f.offset = int64(len(f.node.data))
	}
	end := f.offset + int64(len(p))
	if end > int64(len(f.node.data)) {
		grown := make([]byte, end)
		copy(grown, f.node.data)
		f.node.data = grown
	}
	n := copy(f.node.data[f.offset:], p)
	f.offset += int64(n)
	f.node.contentModified = true
	f.node.modTime = f.adapter.now().UTC()
	if f.node.writeOriginFrame == nil && f.writeOriginFrame != nil {
		f.node.writeOriginFrame = f.writeOriginFrame
	}
	if f.node.parentOpID == "" && f.parentOpID != "" {
		f.node.parentOpID = f.parentOpID
	}
	target := f.node.target
	f.node.mu.Unlock()

	if target.Namespace == pythonruntime.NamespaceWorkspace {
		f.adapter.mu.Lock()
		f.node.mutationSeq = f.adapter.allocSeqLocked()
		f.adapter.recordOverlayEventLocked(f.parentOpID, "write", target, nil, int64(n), f.writeOriginFrame, nil)
		f.adapter.mu.Unlock()
	}
	return n, pythonruntime.ErrnoSuccess
}

func (f *overlayFile) Seek(offset int64, whence int) (int64, pythonruntime.Errno) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return 0, pythonruntime.ErrnoEBADF
	}
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = f.offset
	case io.SeekEnd:
		f.node.mu.Lock()
		base = int64(len(f.node.data))
		f.node.mu.Unlock()
	default:
		return 0, pythonruntime.ErrnoEINVAL
	}
	next := base + offset
	if next < 0 {
		return 0, pythonruntime.ErrnoEINVAL
	}
	f.offset = next
	return f.offset, pythonruntime.ErrnoSuccess
}

func (f *overlayFile) Truncate(size int64) pythonruntime.Errno {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || !f.canWrite() {
		return pythonruntime.ErrnoEBADF
	}
	if size < 0 {
		return pythonruntime.ErrnoEINVAL
	}
	f.node.mu.Lock()
	if f.node.kind == nodeDir {
		f.node.mu.Unlock()
		return pythonruntime.ErrnoEISDIR
	}
	curLen := int64(len(f.node.data))
	if size < curLen {
		f.node.data = append([]byte(nil), f.node.data[:size]...)
	} else if size > curLen {
		grown := make([]byte, size)
		copy(grown, f.node.data)
		f.node.data = grown
	}
	f.node.contentModified = true
	f.node.modTime = f.adapter.now().UTC()
	if f.node.parentOpID == "" && f.parentOpID != "" {
		f.node.parentOpID = f.parentOpID
	}
	target := f.node.target
	f.node.mu.Unlock()

	if target.Namespace == pythonruntime.NamespaceWorkspace {
		f.adapter.mu.Lock()
		f.node.mutationSeq = f.adapter.allocSeqLocked()
		f.adapter.recordOverlayEventLocked(f.parentOpID, "truncate", target, nil, size, f.writeOriginFrame, nil)
		f.adapter.mu.Unlock()
	}
	return pythonruntime.ErrnoSuccess
}

// Sync flushes guest staging durability only; host publication occurs strictly
// during clean-success Settle.
func (f *overlayFile) Sync() pythonruntime.Errno {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return pythonruntime.ErrnoEBADF
	}
	f.node.mu.Lock()
	target := f.node.target
	size := int64(len(f.node.data))
	f.node.mu.Unlock()

	if target.Namespace == pythonruntime.NamespaceWorkspace {
		f.adapter.mu.Lock()
		f.adapter.recordOverlayEventLocked(f.parentOpID, "flush", target, nil, size, f.writeOriginFrame, nil)
		f.adapter.mu.Unlock()
	}
	return pythonruntime.ErrnoSuccess
}

func (f *overlayFile) Stat() (pythonruntime.StatInfo, pythonruntime.Errno) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return pythonruntime.StatInfo{}, pythonruntime.ErrnoEBADF
	}
	return nodeStatInfo(f.node), pythonruntime.ErrnoSuccess
}

func (f *overlayFile) Close() pythonruntime.Errno {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return pythonruntime.ErrnoEBADF
	}
	f.closed = true
	f.mu.Unlock()

	f.node.mu.Lock()
	target := f.node.target
	size := int64(len(f.node.data))
	f.node.mu.Unlock()

	if target.Namespace == pythonruntime.NamespaceWorkspace && f.canWrite() {
		f.adapter.mu.Lock()
		f.adapter.recordOverlayEventLocked(f.parentOpID, "close", target, nil, size, f.writeOriginFrame, nil)
		f.adapter.mu.Unlock()
	}
	return pythonruntime.ErrnoSuccess
}

// nullGuestFile implements pythonruntime.GuestFile for /dev/null.
type nullGuestFile struct {
	target pythonruntime.CanonicalTarget
	now    func() time.Time
	mu     sync.Mutex
	closed bool
}

func (n *nullGuestFile) Read([]byte) (int, pythonruntime.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return 0, pythonruntime.ErrnoEBADF
	}
	return 0, pythonruntime.ErrnoSuccess
}

func (n *nullGuestFile) Write(p []byte) (int, pythonruntime.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return 0, pythonruntime.ErrnoEBADF
	}
	return len(p), pythonruntime.ErrnoSuccess
}

func (n *nullGuestFile) Seek(int64, int) (int64, pythonruntime.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return 0, pythonruntime.ErrnoEBADF
	}
	return 0, pythonruntime.ErrnoSuccess
}

func (n *nullGuestFile) Truncate(int64) pythonruntime.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return pythonruntime.ErrnoEBADF
	}
	return pythonruntime.ErrnoSuccess
}

func (n *nullGuestFile) Sync() pythonruntime.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return pythonruntime.ErrnoEBADF
	}
	return pythonruntime.ErrnoSuccess
}

func (n *nullGuestFile) Stat() (pythonruntime.StatInfo, pythonruntime.Errno) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return pythonruntime.StatInfo{}, pythonruntime.ErrnoEBADF
	}
	return pythonruntime.StatInfo{
		Target:  n.target,
		Name:    "null",
		Mode:    fs.ModeDevice | fs.ModeCharDevice | 0o666,
		ModTime: n.now().UTC(),
	}, pythonruntime.ErrnoSuccess
}

func (n *nullGuestFile) Close() pythonruntime.Errno {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return pythonruntime.ErrnoEBADF
	}
	n.closed = true
	return pythonruntime.ErrnoSuccess
}
