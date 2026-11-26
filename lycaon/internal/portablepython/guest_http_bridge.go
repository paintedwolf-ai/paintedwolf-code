package portablepython

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"
	"github.com/tetratelabs/wazero/sys"
)

const (
	// GuestHTTPBridgeMount is the host-only WASI mount point exposing the
	// synchronous requests.Session host transport bridge to the trusted bootstrap.
	GuestHTTPBridgeMount = "/.pw_http"
	// GuestHTTPBridgeFile is the virtual device filename inside GuestHTTPBridgeMount.
	GuestHTTPBridgeFile = "request"
	// GuestHTTPBridgePath is the full guest path opened by the bootstrap adapter.
	GuestHTTPBridgePath = GuestHTTPBridgeMount + "/" + GuestHTTPBridgeFile

	maxBridgeRequestBytes = 64 * 1024 * 1024
)

// GuestHTTPBridgeFS implements wazero's experimental/sys.FS, exposing a
// per-open virtual request/response file at /.pw_http/request that forwards
// guest requests.Session payloads into pythonadmission.GuestHTTPAdapter.
type GuestHTTPBridgeFS struct {
	experimentalsys.UnimplementedFS

	ctx     context.Context
	adapter *pythonadmission.GuestHTTPAdapter
	latch   *pythonruntime.StopLatch
}

// NewGuestHTTPBridgeFS constructs a WASI virtual FS bridging guest HTTP calls
// into adapter under ctx and latch.
func NewGuestHTTPBridgeFS(
	ctx context.Context,
	adapter *pythonadmission.GuestHTTPAdapter,
	latch *pythonruntime.StopLatch,
) *GuestHTTPBridgeFS {
	if ctx == nil {
		ctx = context.Background()
	}
	return &GuestHTTPBridgeFS{
		ctx:     ctx,
		adapter: adapter,
		latch:   latch,
	}
}

func cleanBridgeRelPath(p string) string {
	cleaned := path.Clean("/" + strings.TrimPrefix(p, "/"))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "" {
		return "."
	}
	return cleaned
}

func (b *GuestHTTPBridgeFS) OpenFile(p string, _ experimentalsys.Oflag, _ fs.FileMode) (experimentalsys.File, experimentalsys.Errno) {
	if b.latch != nil && b.latch.Stopped() {
		return nil, experimentalsys.EACCES
	}
	switch cleanBridgeRelPath(p) {
	case ".":
		return &httpBridgeDirFile{}, 0
	case GuestHTTPBridgeFile:
		return &httpBridgeFile{parent: b}, 0
	default:
		return nil, experimentalsys.ENOENT
	}
}

func (b *GuestHTTPBridgeFS) Stat(p string) (sys.Stat_t, experimentalsys.Errno) {
	switch cleanBridgeRelPath(p) {
	case ".":
		return sys.Stat_t{Dev: 4, Ino: 1, Mode: fs.ModeDir | 0o700, Nlink: 1}, 0
	case GuestHTTPBridgeFile:
		return sys.Stat_t{Dev: 4, Ino: 2, Mode: 0o600, Nlink: 1}, 0
	default:
		return sys.Stat_t{}, experimentalsys.ENOENT
	}
}

func (b *GuestHTTPBridgeFS) Lstat(p string) (sys.Stat_t, experimentalsys.Errno) {
	return b.Stat(p)
}

type httpBridgeDirFile struct {
	experimentalsys.UnimplementedFile
}

func (httpBridgeDirFile) Dev() (uint64, experimentalsys.Errno)    { return 4, 0 }
func (httpBridgeDirFile) Ino() (sys.Inode, experimentalsys.Errno) { return 1, 0 }
func (httpBridgeDirFile) IsDir() (bool, experimentalsys.Errno)    { return true, 0 }
func (httpBridgeDirFile) Stat() (sys.Stat_t, experimentalsys.Errno) {
	return sys.Stat_t{Dev: 4, Ino: 1, Mode: fs.ModeDir | 0o700, Nlink: 1}, 0
}
func (httpBridgeDirFile) Close() experimentalsys.Errno { return 0 }

type bridgeControlPayload struct {
	Action         string `json:"action"`
	StreamHandleID string `json:"stream_handle_id"`
	MaxBytes       int    `json:"max_bytes"`
}

type httpBridgeFile struct {
	experimentalsys.UnimplementedFile

	parent   *GuestHTTPBridgeFS
	mu       sync.Mutex
	reqBuf   bytes.Buffer
	executed bool
	respData []byte
	readPos  int
}

func (f *httpBridgeFile) Dev() (uint64, experimentalsys.Errno)    { return 4, 0 }
func (f *httpBridgeFile) Ino() (sys.Inode, experimentalsys.Errno) { return 2, 0 }
func (f *httpBridgeFile) IsDir() (bool, experimentalsys.Errno)    { return false, 0 }
func (f *httpBridgeFile) IsAppend() bool                          { return false }
func (f *httpBridgeFile) SetAppend(bool) experimentalsys.Errno    { return 0 }
func (f *httpBridgeFile) Stat() (sys.Stat_t, experimentalsys.Errno) {
	f.mu.Lock()
	size := int64(len(f.respData))
	f.mu.Unlock()
	return sys.Stat_t{Dev: 4, Ino: 2, Mode: 0o600, Nlink: 1, Size: size}, 0
}

func (f *httpBridgeFile) Write(p []byte) (int, experimentalsys.Errno) {
	if f.parent.latch != nil && f.parent.latch.Stopped() {
		return 0, experimentalsys.EACCES
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.executed {
		return 0, experimentalsys.EINVAL
	}
	if f.reqBuf.Len()+len(p) > maxBridgeRequestBytes {
		return 0, experimentalsys.EINVAL
	}
	f.reqBuf.Write(p)
	return len(p), 0
}

func (f *httpBridgeFile) Seek(offset int64, whence int) (int64, experimentalsys.Errno) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.executed {
		if errno := f.dispatchLocked(); errno != 0 {
			return 0, errno
		}
	}
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = int64(f.readPos) + offset
	case io.SeekEnd:
		next = int64(len(f.respData)) + offset
	default:
		return 0, experimentalsys.EINVAL
	}
	if next < 0 {
		return 0, experimentalsys.EINVAL
	}
	if next > int64(len(f.respData)) {
		next = int64(len(f.respData))
	}
	f.readPos = int(next)
	return next, 0
}

func (f *httpBridgeFile) Read(p []byte) (int, experimentalsys.Errno) {
	if f.parent.latch != nil && f.parent.latch.Stopped() {
		return 0, experimentalsys.EACCES
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.executed {
		if errno := f.dispatchLocked(); errno != 0 {
			return 0, errno
		}
	}
	if f.readPos >= len(f.respData) {
		return 0, 0
	}
	n := copy(p, f.respData[f.readPos:])
	f.readPos += n
	return n, 0
}

func (f *httpBridgeFile) dispatchLocked() experimentalsys.Errno {
	f.executed = true
	if f.parent.latch != nil && f.parent.latch.Stopped() {
		return experimentalsys.EACCES
	}
	if f.parent.adapter == nil {
		out, _ := json.Marshal(pythonadmission.GuestHTTPResponse{
			Error: "host HTTP transport is not configured",
		})
		f.respData = out
		return 0
	}

	raw := append([]byte(nil), f.reqBuf.Bytes()...)
	var ctrl bridgeControlPayload
	_ = json.Unmarshal(raw, &ctrl)

	var (
		out []byte
		err error
	)
	switch ctrl.Action {
	case "read_chunk":
		out, err = f.parent.adapter.ReadStreamChunk(f.parent.ctx, ctrl.StreamHandleID, ctrl.MaxBytes)
	case "close_stream":
		err = f.parent.adapter.CloseStream(ctrl.StreamHandleID)
		if err == nil {
			out = []byte(`{}`)
		} else {
			out, _ = json.Marshal(pythonadmission.GuestHTTPStreamChunk{Error: err.Error()})
		}
	default:
		out, err = f.parent.adapter.HandleJSON(f.parent.ctx, raw)
	}

	if f.parent.latch != nil && f.parent.latch.Stopped() {
		return experimentalsys.EACCES
	}
	if len(out) == 0 && err != nil {
		out, _ = json.Marshal(pythonadmission.GuestHTTPResponse{
			Error: err.Error(),
		})
	}
	f.respData = out
	f.readPos = 0
	return 0
}

func (f *httpBridgeFile) Truncate(int64) experimentalsys.Errno { return 0 }
func (f *httpBridgeFile) Sync() experimentalsys.Errno          { return 0 }
func (f *httpBridgeFile) Datasync() experimentalsys.Errno      { return 0 }
func (f *httpBridgeFile) Close() experimentalsys.Errno         { return 0 }
