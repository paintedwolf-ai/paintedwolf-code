package pythonruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"sort"
	"strings"
	"testing"
	"time"

	experimentalsys "github.com/tetratelabs/wazero/experimental/sys"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFlagAndErrnoRoundTrip(t *testing.T) {
	oflags := []experimentalsys.Oflag{
		experimentalsys.O_RDONLY,
		experimentalsys.O_WRONLY | experimentalsys.O_CREAT | experimentalsys.O_TRUNC,
		experimentalsys.O_RDWR | experimentalsys.O_APPEND | experimentalsys.O_EXCL,
		experimentalsys.O_RDONLY | experimentalsys.O_DIRECTORY | experimentalsys.O_NOFOLLOW,
	}
	for _, f := range oflags {
		got := OpenFlagsToOflag(OflagToOpenFlags(f))
		if got != f {
			t.Fatalf("oflag round-trip %v -> %v", f, got)
		}
	}

	errnos := []Errno{
		ErrnoSuccess, ErrnoENOENT, ErrnoEEXIST, ErrnoEACCES, ErrnoEPERM,
		ErrnoEISDIR, ErrnoENOTDIR, ErrnoEXDEV, ErrnoEINVAL, ErrnoEBADF,
		ErrnoEROFS, ErrnoEIO,
	}
	for _, e := range errnos {
		got := SysToErrno(ErrnoToSys(e))
		if got != e {
			t.Fatalf("errno round-trip %v -> %v", e, got)
		}
	}
}

func TestWorkspaceSysFSMediationAndStopLatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	latch := NewStopLatch(cancel)
	var outBuf bytes.Buffer
	sealedOut := NewSealedWriter(&outBuf, latch, 0)
	_, err := sealedOut.Write([]byte("before-stop"))
	testutil.FailErr(t, "write before stop", err)

	memWS := newMemWorkspaceAdapter(map[string][]byte{
		"hello.txt": []byte("world"),
	})

	var recordedOps []ReachedOperation
	var recordedOutcomes []OperationOutcome
	obs := &recordingObserver{
		admitFn: func(_ context.Context, op ReachedOperation) (OperationDecision, error) {
			recordedOps = append(recordedOps, op)
			if op.Target.RelPath == "denied.txt" {
				return OperationDecision{Kind: DecisionDeny, Errno: ErrnoEACCES}, nil
			}
			if op.Target.RelPath == "forbidden.txt" {
				return OperationDecision{
					Kind: DecisionStop,
					Stop: &TerminalStop{
						Code:        StopEffectNotAllowed,
						Reason:      "sensitive path write blocked",
						OperationID: op.OperationID,
					},
				}, nil
			}
			return OperationDecision{Kind: DecisionAllow}, nil
		},
		outcomeFn: func(_ context.Context, _ string, outcome OperationOutcome) {
			recordedOutcomes = append(recordedOutcomes, outcome)
		},
	}

	meter := NewWorkMeter(0, 100, latch)
	wfs := NewWorkspaceSysFS(WorkspaceSysFSConfig{
		Context:     ctx,
		ExecutionID: "exec-bridge",
		Namespace:   NamespaceWorkspace,
		RootID:      "root-1",
		Adapter:     memWS,
		Observer:    obs,
		Latch:       latch,
		Meter:       meter,
	})

	// 1. Allowed read crosses observer -> adapter -> outcome.
	f, errno := wfs.OpenFile("hello.txt", experimentalsys.O_RDONLY, 0)
	if errno != 0 {
		t.Fatalf("OpenFile(hello.txt) errno = %v", errno)
	}
	buf := make([]byte, 16)
	n, errno := f.Read(buf)
	if errno != 0 || string(buf[:n]) != "world" {
		t.Fatalf("Read = %q (errno=%v), want %q", string(buf[:n]), errno, "world")
	}
	if errno := f.Close(); errno != 0 {
		t.Fatalf("Close errno = %v", errno)
	}

	// 2. Recoverable deny returns EACCES without tripping StopLatch.
	_, errno = wfs.OpenFile("denied.txt", experimentalsys.O_RDONLY, 0)
	if errno != experimentalsys.EACCES {
		t.Fatalf("OpenFile(denied.txt) errno = %v, want EACCES", errno)
	}
	if latch.Stopped() {
		t.Fatal("DecisionDeny must not trip StopLatch")
	}

	// 3. Readdir streams directory entries and resets on Seek(0, SeekStart).
	dirF, errno := wfs.OpenFile(".", experimentalsys.O_RDONLY|experimentalsys.O_DIRECTORY, 0)
	if errno != 0 {
		t.Fatalf("OpenFile(.) errno = %v", errno)
	}
	entries, errno := dirF.Readdir(10)
	if errno != 0 || len(entries) != 1 || entries[0].Name != "hello.txt" {
		t.Fatalf("Readdir = %+v (errno=%v)", entries, errno)
	}
	if _, errno := dirF.Seek(0, io.SeekStart); errno != 0 {
		t.Fatalf("dir Seek errno = %v", errno)
	}
	entries2, errno := dirF.Readdir(10)
	if errno != 0 || len(entries2) != 1 {
		t.Fatalf("Readdir after seek = %+v (errno=%v)", entries2, errno)
	}
	_ = dirF.Close()

	// 4. DecisionStop latches StopLatch, cancels ctx, and seals stdout immediately.
	_, errno = wfs.OpenFile("forbidden.txt", experimentalsys.O_WRONLY|experimentalsys.O_CREAT, 0o644)
	if errno != experimentalsys.EACCES {
		t.Fatalf("OpenFile(forbidden.txt) errno = %v, want EACCES", errno)
	}
	if !latch.Stopped() || latch.Stop().Code != StopEffectNotAllowed {
		t.Fatalf("expected StopEffectNotAllowed latched, got %+v", latch.Stop())
	}
	if ctx.Err() == nil {
		t.Fatal("expected ctx cancelled on DecisionStop")
	}
	if _, err := sealedOut.Write([]byte("after-stop")); !errors.Is(err, ErrStreamSealed) {
		t.Fatalf("expected ErrStreamSealed after DecisionStop, got %v", err)
	}
	if outBuf.String() != "before-stop" {
		t.Fatalf("stdout leaked post-stop bytes: %q", outBuf.String())
	}

	// 5. Subsequent FS calls fail closed without consulting observer again.
	beforeOps := len(recordedOps)
	if _, errno := wfs.Stat("hello.txt"); errno != experimentalsys.EACCES {
		t.Fatalf("Stat after stop errno = %v, want EACCES", errno)
	}
	if len(recordedOps) != beforeOps {
		t.Fatalf("observer was invoked after StopLatch tripped")
	}
	if len(recordedOutcomes) == 0 || meter.HostWork() == 0 {
		t.Fatalf("expected recorded outcomes and host work > 0, got outcomes=%d hostWork=%d", len(recordedOutcomes), meter.HostWork())
	}
}

func TestHostWorkLimitTriggersStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	latch := NewStopLatch(cancel)
	meter := NewWorkMeter(0, 2, latch)
	memWS := newMemWorkspaceAdapter(map[string][]byte{"a.txt": []byte("x")})
	wfs := NewWorkspaceSysFS(WorkspaceSysFSConfig{
		Context:     ctx,
		ExecutionID: "exec-host-limit",
		Adapter:     memWS,
		Latch:       latch,
		Meter:       meter,
	})

	_, _ = wfs.Stat("a.txt") // hostWork = 1
	_, _ = wfs.Stat("a.txt") // hostWork = 2 (at limit)
	if latch.Stopped() {
		t.Fatal("at-limit host work should not stop")
	}
	_, _ = wfs.Stat("a.txt") // hostWork = 3 (over limit -> stops)
	if !latch.Stopped() || latch.Stop().Code != StopWorkLimitExceeded {
		t.Fatalf("expected StopWorkLimitExceeded, got %+v", latch.Stop())
	}
}

func TestNullDeviceFS(t *testing.T) {
	var nd NullDeviceFS
	f, errno := nd.OpenFile("null", experimentalsys.O_RDWR, 0)
	if errno != 0 {
		t.Fatalf("OpenFile(null) errno = %v", errno)
	}
	defer f.Close()

	n, errno := f.Write([]byte("discard me"))
	if errno != 0 || n != 10 {
		t.Fatalf("Write = (%d, %v), want (10, 0)", n, errno)
	}
	buf := make([]byte, 8)
	n, errno = f.Read(buf)
	if errno != 0 || n != 0 {
		t.Fatalf("Read = (%d, %v), want (0, 0)", n, errno)
	}
	if _, errno := nd.OpenFile("other", experimentalsys.O_RDONLY, 0); errno != experimentalsys.ENOENT {
		t.Fatalf("OpenFile(other) errno = %v, want ENOENT", errno)
	}
	if errno := nd.Mkdir("sub", 0o755); errno != experimentalsys.EROFS {
		t.Fatalf("Mkdir errno = %v, want EROFS", errno)
	}
}

func TestGuestWorkLimitInterruptsWasmCalls(t *testing.T) {
	ctx := context.Background()
	rt, err := NewWazeroRuntime(ctx, RuntimeConfig{
		WasmBytes: wasmRecursiveCallsModule(),
	})
	testutil.FailErr(t, "new wazero runtime for guest work limit", err)
	defer rt.Close(ctx)

	res, err := rt.Execute(ctx, Invocation{
		ExecutionID: "guest-work-limit",
		Mode:        ModeCode,
		Code:        "pass",
		Limits: ResourceLimits{
			MaxGuestWork: 5,
			Timeout:      2 * time.Second,
		},
	})
	testutil.FailErr(t, "execute over guest work limit", err)
	if res.CleanCompletion {
		t.Fatal("expected CleanCompletion=false when guest work exceeds MaxGuestWork")
	}
	if res.Stop == nil || res.Stop.Code != StopWorkLimitExceeded {
		t.Fatalf("expected StopWorkLimitExceeded, got %+v (guestWork=%d)", res.Stop, res.Usage.GuestWork)
	}
	if res.Usage.GuestWork <= 5 {
		t.Fatalf("expected GuestWork > 5, got %d", res.Usage.GuestWork)
	}
}

// --- In-memory WorkspaceAdapter and Observer test helpers ---

type recordingObserver struct {
	admitFn   func(context.Context, ReachedOperation) (OperationDecision, error)
	outcomeFn func(context.Context, string, OperationOutcome)
}

func (o *recordingObserver) Admit(ctx context.Context, op ReachedOperation) (OperationDecision, error) {
	if o.admitFn != nil {
		return o.admitFn(ctx, op)
	}
	return OperationDecision{Kind: DecisionAllow}, nil
}

func (o *recordingObserver) RecordOutcome(ctx context.Context, opID string, outcome OperationOutcome) {
	if o.outcomeFn != nil {
		o.outcomeFn(ctx, opID, outcome)
	}
}

type memWorkspaceAdapter struct {
	files map[string][]byte
}

func newMemWorkspaceAdapter(files map[string][]byte) *memWorkspaceAdapter {
	cp := make(map[string][]byte, len(files))
	for k, v := range files {
		cp[k] = append([]byte(nil), v...)
	}
	return &memWorkspaceAdapter{files: cp}
}

func (m *memWorkspaceAdapter) isDir(p string) bool {
	if p == "." {
		return true
	}
	prefix := p + "/"
	for k := range m.files {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

func (m *memWorkspaceAdapter) Open(_ context.Context, req OpenRequest) (GuestFile, Errno) {
	if m.isDir(req.Path) {
		return &memGuestFile{name: req.Path, isDir: true}, ErrnoSuccess
	}
	data, ok := m.files[req.Path]
	if !ok {
		if req.Flags&OpenCreate == 0 {
			return nil, ErrnoENOENT
		}
		m.files[req.Path] = nil
		data = nil
	}
	return &memGuestFile{name: req.Path, data: append([]byte(nil), data...)}, ErrnoSuccess
}

func (m *memWorkspaceAdapter) Stat(_ context.Context, p string) (StatInfo, Errno) {
	if m.isDir(p) {
		return StatInfo{Name: p, IsDir: true, Mode: fs.ModeDir | 0o755, Inode: 1}, ErrnoSuccess
	}
	data, ok := m.files[p]
	if !ok {
		return StatInfo{}, ErrnoENOENT
	}
	return StatInfo{Name: p, Size: int64(len(data)), Mode: 0o644, Inode: 2}, ErrnoSuccess
}

func (m *memWorkspaceAdapter) Lstat(ctx context.Context, p string) (StatInfo, Errno) {
	return m.Stat(ctx, p)
}

func (m *memWorkspaceAdapter) ReadDir(_ context.Context, p string) ([]DirEntry, Errno) {
	if !m.isDir(p) {
		return nil, ErrnoENOENT
	}
	seen := make(map[string]DirEntry)
	prefix := ""
	if p != "." {
		prefix = p + "/"
	}
	for k := range m.files {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		if idx := strings.IndexByte(rest, '/'); idx >= 0 {
			dirName := rest[:idx]
			seen[dirName] = DirEntry{Name: dirName, IsDir: true, Mode: fs.ModeDir | 0o755}
		} else {
			seen[rest] = DirEntry{Name: rest, Mode: 0o644}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]DirEntry, 0, len(names))
	for _, name := range names {
		out = append(out, seen[name])
	}
	return out, ErrnoSuccess
}

func (m *memWorkspaceAdapter) Mkdir(context.Context, string, fs.FileMode) Errno { return ErrnoSuccess }
func (m *memWorkspaceAdapter) Unlink(_ context.Context, p string) Errno {
	delete(m.files, p)
	return ErrnoSuccess
}
func (m *memWorkspaceAdapter) Rmdir(context.Context, string) Errno { return ErrnoSuccess }
func (m *memWorkspaceAdapter) Rename(_ context.Context, oldP, newP string) Errno {
	data, ok := m.files[oldP]
	if !ok {
		return ErrnoENOENT
	}
	m.files[newP] = data
	delete(m.files, oldP)
	return ErrnoSuccess
}
func (m *memWorkspaceAdapter) Readlink(context.Context, string) (string, Errno) {
	return "", ErrnoEINVAL
}
func (m *memWorkspaceAdapter) Settle(_ context.Context, clean bool) (*SettlementReport, error) {
	return &SettlementReport{CleanCompletion: clean}, nil
}

type memGuestFile struct {
	name  string
	isDir bool
	data  []byte
	pos   int64
}

func (f *memGuestFile) Read(p []byte) (int, Errno) {
	if f.isDir {
		return 0, ErrnoEISDIR
	}
	if f.pos >= int64(len(f.data)) {
		return 0, ErrnoSuccess
	}
	n := copy(p, f.data[f.pos:])
	f.pos += int64(n)
	return n, ErrnoSuccess
}

func (f *memGuestFile) Write(p []byte) (int, Errno) {
	if f.isDir {
		return 0, ErrnoEISDIR
	}
	f.data = append(f.data, p...)
	f.pos = int64(len(f.data))
	return len(p), ErrnoSuccess
}

func (f *memGuestFile) Seek(offset int64, whence int) (int64, Errno) {
	switch whence {
	case io.SeekStart:
		f.pos = offset
	case io.SeekCurrent:
		f.pos += offset
	case io.SeekEnd:
		f.pos = int64(len(f.data)) + offset
	default:
		return 0, ErrnoEINVAL
	}
	return f.pos, ErrnoSuccess
}

func (f *memGuestFile) Truncate(size int64) Errno {
	if size < 0 {
		return ErrnoEINVAL
	}
	if size < int64(len(f.data)) {
		f.data = f.data[:size]
	}
	return ErrnoSuccess
}

func (f *memGuestFile) Sync() Errno { return ErrnoSuccess }
func (f *memGuestFile) Stat() (StatInfo, Errno) {
	return StatInfo{Name: f.name, Size: int64(len(f.data)), IsDir: f.isDir, Mode: 0o644, Inode: 2}, ErrnoSuccess
}
func (f *memGuestFile) Close() Errno { return ErrnoSuccess }

// wasmRecursiveCallsModule builds a Wasm module where _start calls helper() in
// a loop so each iteration invokes a guest function and charges guest work.
func wasmRecursiveCallsModule() []byte {
	mod := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	// Type 0: () -> ()
	mod = append(mod, wasmSection(1, []byte{0x01, 0x60, 0x00, 0x00})...)
	// Functions: 0 = _start (type 0), 1 = helper (type 0)
	mod = append(mod, wasmSection(3, []byte{0x02, 0x00, 0x00})...)
	// Memory: 1 page
	mod = append(mod, wasmSection(5, []byte{0x01, 0x00, 0x01})...)
	// Exports: "memory", "_start"
	exp := []byte{0x02}
	exp = append(exp, wasmString("memory")...)
	exp = append(exp, 0x02, 0x00)
	exp = append(exp, wasmString("_start")...)
	exp = append(exp, 0x00, 0x00)
	mod = append(mod, wasmSection(7, exp)...)
	// Code: func 0 loops calling func 1; func 1 is nop/return
	body0 := []byte{
		0x00,       // 0 locals
		0x03, 0x40, // loop
		0x10, 0x01, // call 1 (helper)
		0x0c, 0x00, // br 0
		0x0b,       // end loop
		0x0b,       // end func
	}
	body1 := []byte{0x00, 0x0b}
	code := []byte{0x02}
	code = append(code, encodeULEB128(uint32(len(body0)))...)
	code = append(code, body0...)
	code = append(code, encodeULEB128(uint32(len(body1)))...)
	code = append(code, body1...)
	mod = append(mod, wasmSection(10, code)...)
	return mod
}
