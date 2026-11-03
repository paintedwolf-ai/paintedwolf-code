package fuselinux

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

// wireServer drives the real server loop over a packet socket standing in for /dev/fuse, so the
// protocol's orderings can be produced exactly instead of waiting for a kernel race.
type wireServer struct {
	t       *testing.T
	srv     *Server
	core    *layeredfs.Core
	peer    int
	trunk   string
	backing string
	next    uint64
	replies map[uint64]wireReply
	syncErr atomic.Pointer[error]
}

type wireReply struct {
	errno   int32
	payload []byte
}

func newWireServer(t *testing.T, cfg ServerConfig) *wireServer {
	t.Helper()
	tmp := t.TempDir()
	w := &wireServer{t: t, trunk: filepath.Join(tmp, "trunk"), backing: filepath.Join(tmp, "backing"), next: 2, replies: map[uint64]wireReply{}}
	for _, d := range []string{w.trunk, w.backing} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	core, err := layeredfs.New(layeredfs.Config{
		ViewID: "wire", BackingDir: w.backing, ActiveRootID: "primary",
		Roots: []layeredfs.RootSpec{{ID: "primary", Label: "primary", TrunkPath: w.trunk, IsPrimary: true}},
		FaultHooks: &layeredfs.FaultHooks{JournalSyncHook: func(string) error {
			if injected := w.syncErr.Load(); injected != nil {
				return *injected
			}
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("layeredfs.New: %v", err)
	}
	w.core = core
	socks, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	var stop [2]int
	if err := unix.Pipe2(stop[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatalf("pipe2: %v", err)
	}
	w.peer = socks[1]
	if deadline, ok := t.Deadline(); ok {
		tv := unix.NsecToTimeval(time.Until(deadline.Add(-5 * time.Second)).Nanoseconds())
		if err := unix.SetsockoptTimeval(w.peer, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
			t.Fatalf("SO_RCVTIMEO: %v", err)
		}
	}
	cfg.RootID, cfg.BackingDir = "primary", w.backing
	w.srv = newServerInstance(cfg, core, socks[0], stop)
	w.srv.start()
	t.Cleanup(func() {
		_ = w.srv.Close()
		_ = unix.Close(w.peer)
		_ = core.ReleaseView()
	})
	return w
}

func (w *wireServer) nextUnique() uint64 {
	u := w.next
	w.next += 2
	return u
}

func (w *wireServer) sendWith(unique uint64, opcode uint32, nodeID uint64, body []byte) {
	w.t.Helper()
	pkt := make([]byte, fuseHeaderInSize+len(body))
	binary.LittleEndian.PutUint32(pkt[0:4], uint32(len(pkt)))
	binary.LittleEndian.PutUint32(pkt[4:8], opcode)
	binary.LittleEndian.PutUint64(pkt[8:16], unique)
	binary.LittleEndian.PutUint64(pkt[16:24], nodeID)
	copy(pkt[fuseHeaderInSize:], body)
	if _, err := unix.Write(w.peer, pkt); err != nil {
		w.t.Fatalf("send opcode %d: %v", opcode, err)
	}
}

func (w *wireServer) send(opcode uint32, nodeID uint64, body []byte) uint64 {
	w.t.Helper()
	u := w.nextUnique()
	w.sendWith(u, opcode, nodeID, body)
	return u
}

func (w *wireServer) reply(unique uint64) wireReply {
	w.t.Helper()
	for {
		if r, ok := w.replies[unique]; ok {
			delete(w.replies, unique)
			return r
		}
		buf := make([]byte, 256*1024)
		n, err := unix.Read(w.peer, buf)
		if err != nil || n < fuseHeaderOutSize {
			w.t.Fatalf("waiting for reply to %d: n=%d err=%v", unique, n, err)
		}
		got := binary.LittleEndian.Uint64(buf[8:16])
		w.replies[got] = wireReply{errno: int32(binary.LittleEndian.Uint32(buf[4:8])), payload: append([]byte(nil), buf[fuseHeaderOutSize:n]...)}
	}
}

func (w *wireServer) call(opcode uint32, nodeID uint64, body []byte) wireReply {
	w.t.Helper()
	return w.reply(w.send(opcode, nodeID, body))
}

func (w *wireServer) wantErrno(what string, r wireReply, want syscall.Errno) {
	w.t.Helper()
	if r.errno != -int32(want) {
		w.t.Fatalf("%s: errno %d, want -%d (%v)", what, r.errno, want, want)
	}
}

func (w *wireServer) await(desc string, cond func() bool) {
	w.t.Helper()
	deadline, bounded := w.t.Deadline()
	for !cond() {
		if bounded && time.Now().After(deadline.Add(-5*time.Second)) {
			w.t.Fatalf("never observed: %s", desc)
		}
		time.Sleep(time.Millisecond)
	}
}

func cName(names ...string) []byte {
	var b bytes.Buffer
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(0)
	}
	return b.Bytes()
}

func u64(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }

func (w *wireServer) lookup(name string) uint64 {
	w.t.Helper()
	r := w.call(fuseOpLookup, fuseRootID, cName(name))
	w.wantErrno("lookup "+name, r, 0)
	return binary.LittleEndian.Uint64(r.payload[0:8])
}

func (w *wireServer) open(nodeID uint64, flags uint32) uint64 {
	w.t.Helper()
	body := make([]byte, 8)
	binary.LittleEndian.PutUint32(body[0:4], flags)
	r := w.call(fuseOpOpen, nodeID, body)
	w.wantErrno("open", r, 0)
	return binary.LittleEndian.Uint64(r.payload[0:8])
}

func (w *wireServer) seed(name, content string) {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.trunk, name), []byte(content), 0o644); err != nil {
		w.t.Fatalf("seed %s: %v", name, err)
	}
}

func lockBody(owner, start, end uint64, typ uint32) []byte {
	b := make([]byte, 48)
	binary.LittleEndian.PutUint64(b[8:16], owner)
	binary.LittleEndian.PutUint64(b[16:24], start)
	binary.LittleEndian.PutUint64(b[24:32], end)
	binary.LittleEndian.PutUint32(b[32:36], typ)
	binary.LittleEndian.PutUint32(b[36:40], uint32(owner))
	return b
}

func TestClassificationKeepsLockReleaseOutOfEveryBoundedLane(t *testing.T) {
	cases := []struct {
		name   string
		opcode uint32
		body   []byte
		want   requestLane
	}{
		{"nonblocking lock and unlock", fuseOpSetlk, lockBody(1, 0, 1, syscall.F_WRLCK), laneInline},
		{"blocking-form unlock", fuseOpSetlkw, lockBody(1, 0, 1, syscall.F_UNLCK), laneInline},
		{"lock query", fuseOpGetlk, lockBody(1, 0, 1, syscall.F_WRLCK), laneInline},
		{"close-time lock release", fuseOpFlush, nil, laneInline},
		{"blocking acquire", fuseOpSetlkw, lockBody(1, 0, 1, syscall.F_WRLCK), laneLockWait},
		{"malformed blocking acquire", fuseOpSetlkw, nil, laneLockWait},
		{"handle release", fuseOpRelease, nil, laneControl},
		{"read", fuseOpRead, nil, laneOrdinary},
		{"write", fuseOpWrite, nil, laneOrdinary},
	}
	for _, c := range cases {
		if got := classifyRequest(c.opcode, c.body); got != c.want {
			t.Errorf("%s: lane %d, want %d", c.name, got, c.want)
		}
	}
}

func TestInterruptForAnsweredRequestAsksKernelToRetryAndKeepsNoState(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	answered := w.send(fuseOpStatfs, fuseRootID, nil)
	w.wantErrno("statfs", w.reply(answered), 0)
	w.await("answered request forgotten", func() bool {
		w.srv.reqMu.Lock()
		defer w.srv.reqMu.Unlock()
		return len(w.srv.inFlightReqs) == 0
	})

	for range 1024 {
		interrupt := w.nextUnique() | 1
		w.sendWith(interrupt, fuseOpInterrupt, 0, u64(answered))
		w.wantErrno("interrupt of answered request", w.reply(interrupt), syscall.EAGAIN)
	}
	w.srv.reqMu.Lock()
	defer w.srv.reqMu.Unlock()
	if n := len(w.srv.inFlightReqs); n != 0 {
		t.Fatalf("interrupts left %d request records", n)
	}
}

func TestInterruptCancelsOnlyItsWaiterWhichNeverAcquires(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	w.seed("f", "x")
	node := w.lookup("f")
	w.wantErrno("hold", w.call(fuseOpSetlk, node, lockBody(1, 0, 99, syscall.F_WRLCK)), 0)
	waiterA := w.send(fuseOpSetlkw, node, lockBody(2, 0, 49, syscall.F_WRLCK))
	waiterB := w.send(fuseOpSetlkw, node, lockBody(3, 50, 99, syscall.F_WRLCK))
	w.await("two waiters", func() bool { return w.srv.LockWaiterCountForTest() == 2 })

	// The conflict is released right after the interrupt, so the canceled waiter wakes to a free
	// range. Identity is checked before the lock table, so it must still not acquire.
	interrupt := w.nextUnique() | 1
	w.sendWith(interrupt, fuseOpInterrupt, 0, u64(waiterA))
	w.wantErrno("release hold", w.call(fuseOpSetlk, node, lockBody(1, 0, 99, syscall.F_UNLCK)), 0)
	w.wantErrno("interrupted waiter", w.reply(waiterA), syscall.EINTR)
	w.wantErrno("other waiter", w.reply(waiterB), 0)
	w.await("waiters settled", func() bool { return w.srv.LockWaiterCountForTest() == 0 })

	r := w.call(fuseOpGetlk, node, lockBody(9, 0, 49, syscall.F_WRLCK))
	w.wantErrno("getlk", r, 0)
	if typ := binary.LittleEndian.Uint32(r.payload[16:20]); typ != syscall.F_UNLCK {
		t.Fatalf("canceled waiter holds [0,49]: lock type %d", typ)
	}
}

func TestWaiterLimitIsENOLCKAndLeavesFileOperationsServed(t *testing.T) {
	w := newWireServer(t, ServerConfig{MaxConcurrentRequests: 1, MaxLockWaiters: 1})
	w.seed("f", "x")
	node := w.lookup("f")
	w.wantErrno("hold", w.call(fuseOpSetlk, node, lockBody(1, 0, 99, syscall.F_WRLCK)), 0)
	waiter := w.send(fuseOpSetlkw, node, lockBody(2, 0, 49, syscall.F_WRLCK))
	w.await("waiter parked", func() bool { return w.srv.LockWaiterCountForTest() == 1 })

	w.wantErrno("over limit", w.call(fuseOpSetlkw, node, lockBody(3, 0, 49, syscall.F_WRLCK)), syscall.ENOLCK)
	w.wantErrno("uncontended blocking acquire", w.call(fuseOpSetlkw, node, lockBody(3, 200, 201, syscall.F_WRLCK)), 0)
	for range 8 {
		w.wantErrno("file operation", w.call(fuseOpStatfs, fuseRootID, nil), 0)
	}
	w.wantErrno("release hold", w.call(fuseOpSetlk, node, lockBody(1, 0, 99, syscall.F_UNLCK)), 0)
	w.wantErrno("waiter", w.reply(waiter), 0)
}

// holdWorker occupies the only worker inside a mutation until the returned function runs.
func holdWorker(t *testing.T) (*wireServer, uint64, func()) {
	t.Helper()
	entered, unblock := make(chan struct{}), make(chan struct{})
	w := newWireServer(t, ServerConfig{MaxConcurrentRequests: 1, MaxLockWaiters: 2, OnMutationHook: func(uint32, string) {
		close(entered)
		<-unblock
	}})
	held := w.send(fuseOpMkdir, fuseRootID, append(make([]byte, 8), cName("held")...))
	<-entered
	return w, held, func() { close(unblock) }
}

func TestSaturatedWorkersStillServeUnlockInterruptAndRelease(t *testing.T) {
	w, held, unblock := holdWorker(t)
	w.seed("f", "x")
	queued := w.send(fuseOpStatfs, fuseRootID, nil)
	cancelled := w.send(fuseOpStatfs, fuseRootID, nil)
	cancelInterrupt := w.nextUnique() | 1
	w.sendWith(cancelInterrupt, fuseOpInterrupt, 0, u64(cancelled))

	// Every reply here arrives while the only worker is still held, because none of them needs it.
	const node = fuseRootID
	w.wantErrno("hold", w.call(fuseOpSetlk, node, lockBody(1, 0, 99, syscall.F_WRLCK)), 0)
	first := w.send(fuseOpSetlkw, node, lockBody(2, 0, 49, syscall.F_WRLCK))
	second := w.send(fuseOpSetlkw, node, lockBody(3, 0, 49, syscall.F_WRLCK))
	w.await("waiters parked", func() bool { return w.srv.LockWaiterCountForTest() == 2 })
	interrupt := w.nextUnique() | 1
	w.sendWith(interrupt, fuseOpInterrupt, 0, u64(first))
	w.wantErrno("interrupted waiter", w.reply(first), syscall.EINTR)
	w.wantErrno("unlock", w.call(fuseOpSetlkw, node, lockBody(1, 0, 99, syscall.F_UNLCK)), 0)
	w.wantErrno("granted waiter", w.reply(second), 0)
	w.wantErrno("flush releases", w.call(fuseOpFlush, node, append(make([]byte, 16), u64(3)...)), 0)
	w.wantErrno("release", w.call(fuseOpRelease, node, make([]byte, 24)), 0)

	unblock()
	w.wantErrno("held mutation", w.reply(held), 0)
	w.wantErrno("queued request", w.reply(queued), 0)
	w.wantErrno("request interrupted while queued", w.reply(cancelled), syscall.EINTR)
}

func TestCloseSettlesWaitersQueuedAndGatedRequests(t *testing.T) {
	w, _, unblock := holdWorker(t)
	w.send(fuseOpStatfs, fuseRootID, nil)
	w.wantErrno("hold", w.call(fuseOpSetlk, fuseRootID, lockBody(1, 0, 99, syscall.F_WRLCK)), 0)
	w.send(fuseOpSetlkw, fuseRootID, lockBody(2, 0, 49, syscall.F_WRLCK))
	w.await("waiter parked", func() bool { return w.srv.LockWaiterCountForTest() == 1 })
	unblock()
	if err := w.srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := w.srv.LockWaiterCountForTest(); got != 0 {
		t.Fatalf("waiters after Close = %d", got)
	}
}

func TestCloseAfterConnectionEndsReleasesDescriptors(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	fd, stopRead, stopWrite := w.srv.fd, w.srv.stopPipe[0], w.srv.stopPipe[1]
	if err := unix.Shutdown(w.peer, unix.SHUT_RDWR); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	w.await("server noticed the ended connection", func() bool { return !w.srv.IsAlive() })
	if err := w.srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, d := range []int{fd, stopRead, stopWrite} {
		if _, err := unix.FcntlInt(uintptr(d), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Fatalf("descriptor %d still open after Close: %v", d, err)
		}
	}
}

func TestInitGrantsOnlyPinnedCapabilitiesAndRefusesOldKernels(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	initBody := func(major, minor, flags uint32) []byte {
		b := make([]byte, 16)
		binary.LittleEndian.PutUint32(b[0:4], major)
		binary.LittleEndian.PutUint32(b[4:8], minor)
		binary.LittleEndian.PutUint32(b[8:12], DefaultMaxReadAhead)
		binary.LittleEndian.PutUint32(b[12:16], flags)
		return b
	}
	w.wantErrno("old kernel", w.call(fuseOpInit, 0, initBody(FUSEProtocolMajor, FUSEProtocolMinor-1, 0)), syscall.EPROTO)
	if w.srv.NegotiatedProtocol() != nil {
		t.Fatalf("refused INIT must not record a protocol")
	}
	r := w.call(fuseOpInit, 0, initBody(FUSEProtocolMajor, 41, ^uint32(0)))
	w.wantErrno("init", r, 0)
	want := uint32(fuseCapPOSIXLocks | fuseCapAtomicOTrunc | fuseCapBigWrites | fuseCapFlockLocks)
	if got := binary.LittleEndian.Uint32(r.payload[12:16]); got != want {
		t.Fatalf("granted flags %#x, want exactly %#x", got, want)
	}
	if got := binary.LittleEndian.Uint32(r.payload[4:8]); got != FUSEProtocolMinor {
		t.Fatalf("replied minor %d, want %d", got, FUSEProtocolMinor)
	}
	if p := w.srv.NegotiatedProtocol(); p == nil || p.KernelMinor != 41 || p.GrantedFlags != want {
		t.Fatalf("recorded protocol %+v", p)
	}
}

func TestFsyncAcknowledgesOnlyEstablishedDurability(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	fsync := func(fh uint64) wireReply { return w.call(fuseOpFsync, fuseRootID, append(u64(fh), make([]byte, 8)...)) }
	injected := error(syscall.EIO)

	w.wantErrno("unknown descriptor", fsync(1<<40), syscall.EBADF)

	w.seed("f", "x")
	node := w.lookup("f")
	rw := w.open(node, syscall.O_RDWR)
	w.wantErrno("clean fsync", fsync(rw), 0)
	w.syncErr.Store(&injected)
	w.wantErrno("failed backing sync", fsync(rw), syscall.EIO)
	w.wantErrno("failed journal sync", w.call(fuseOpFsyncdir, fuseRootID, make([]byte, 16)), syscall.EIO)
	w.syncErr.Store(nil)
	w.wantErrno("clean fsyncdir", w.call(fuseOpFsyncdir, fuseRootID, make([]byte, 16)), 0)

	// A read-only descriptor still covers the file: its private content must really be synced.
	ro := w.open(node, syscall.O_RDONLY)
	var synced atomic.Int32
	w.srv.syncBacking = func(interface{ Sync() error }) error {
		synced.Add(1)
		return syscall.ENOSPC
	}
	w.wantErrno("failed read-only descriptor sync", fsync(ro), syscall.ENOSPC)
	if synced.Load() != 1 {
		t.Fatalf("read-only fsync reached backing %d times", synced.Load())
	}
}

func TestDescriptorOperationsNeverReachAReplacementAtTheSamePath(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	w.seed("old", "old-content")
	w.seed("new", "new")
	oldNode := w.lookup("old")
	oldFH := w.open(oldNode, syscall.O_RDWR)

	rename := append(u64(fuseRootID), cName("new", "old")...)
	w.wantErrno("rename over", w.call(fuseOpRename, fuseRootID, rename), 0)

	getattr := append(make([]byte, 8), u64(oldFH)...)
	r := w.call(fuseOpGetattr, oldNode, getattr)
	w.wantErrno("getattr of replaced object", r, 0)
	if size := binary.LittleEndian.Uint64(r.payload[24:32]); size != uint64(len("old-content")) {
		t.Fatalf("replaced object reports size %d: its attributes came from the replacement", size)
	}

	truncate := make([]byte, 88)
	binary.LittleEndian.PutUint32(truncate[0:4], fattrSize|fattrFH)
	binary.LittleEndian.PutUint64(truncate[8:16], oldFH)
	w.wantErrno("ftruncate of replaced object", w.call(fuseOpSetattr, oldNode, truncate), syscall.ESTALE)

	got, err := w.core.ReadFile(layeredfs.QualifiedPath{RootID: "primary", Rel: "old"})
	if err != nil || string(got) != "new" {
		t.Fatalf("replacement changed by a descriptor on the old object: %q err=%v", got, err)
	}

	stale := make([]byte, fuseWriteInHeaderSize+1)
	binary.LittleEndian.PutUint64(stale[0:8], 1<<40)
	binary.LittleEndian.PutUint32(stale[16:20], 1)
	w.wantErrno("write to unknown descriptor", w.call(fuseOpWrite, w.lookup("old"), stale), syscall.EBADF)
	w.wantErrno("read of unknown descriptor", w.call(fuseOpRead, oldNode, append(u64(1<<40), make([]byte, 32)...)), syscall.EBADF)
}

func TestSetxattrHonorsCreateAndReplaceFlags(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	w.seed("f", "x")
	node := w.lookup("f")
	setxattr := func(flags uint32) wireReply {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint32(b[0:4], 1)
		binary.LittleEndian.PutUint32(b[4:8], flags)
		b = append(b, cName("user.k")...)
		return w.call(fuseOpSetxattr, node, append(b, 'v'))
	}
	w.wantErrno("replace of missing attribute", setxattr(unix.XATTR_REPLACE), syscall.ENODATA)
	w.wantErrno("create", setxattr(unix.XATTR_CREATE), 0)
	w.wantErrno("create of existing attribute", setxattr(unix.XATTR_CREATE), syscall.EEXIST)
	w.wantErrno("replace", setxattr(unix.XATTR_REPLACE), 0)
	w.wantErrno("unconditional", setxattr(0), 0)
}

func TestBarrierHoldsMutationsAndUnknownOpcodesNeverReachTheGate(t *testing.T) {
	var hooked atomic.Int32
	w := newWireServer(t, ServerConfig{OnMutationHook: func(uint32, string) { hooked.Add(1) }})
	release, err := w.srv.BeginBarrierDrain(t.Context(), "wire")
	if err != nil {
		t.Fatalf("BeginBarrierDrain: %v", err)
	}
	const fuseOpFallocate = 43
	w.wantErrno("unimplemented opcode during barrier", w.call(fuseOpFallocate, fuseRootID, make([]byte, 32)), syscall.ENOSYS)
	if hooked.Load() != 0 {
		t.Fatalf("unimplemented opcode was admitted as a mutation")
	}

	mkdir := func(name string) uint64 {
		return w.send(fuseOpMkdir, fuseRootID, append(make([]byte, 8), cName(name)...))
	}
	interrupted, held := mkdir("interrupted"), mkdir("held")
	w.await("both mutations held", func() bool { return w.srv.GatedMutationsForTest() == 2 })
	w.wantErrno("reads continue during barrier", w.call(fuseOpStatfs, fuseRootID, nil), 0)

	interrupt := w.nextUnique() | 1
	w.sendWith(interrupt, fuseOpInterrupt, 0, u64(interrupted))
	w.wantErrno("mutation interrupted at the gate", w.reply(interrupted), syscall.EINTR)

	release()
	w.wantErrno("mutation after barrier", w.reply(held), 0)
	if _, err := w.core.Lstat(layeredfs.QualifiedPath{RootID: "primary", Rel: "interrupted"}); !os.IsNotExist(err) {
		t.Fatalf("interrupted mutation took effect: %v", err)
	}
}

func TestDirectoryStreamSurvivesRemovalAndRewindRefreshes(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	const total = 64
	for i := range total {
		w.seed(string(rune('a'+i%26))+string(rune('a'+i/26)), "x")
	}
	r := w.call(fuseOpOpendir, fuseRootID, make([]byte, 8))
	w.wantErrno("opendir", r, 0)
	fh := binary.LittleEndian.Uint64(r.payload[0:8])

	readBatch := func(offset uint64) ([]string, uint64) {
		body := make([]byte, 40)
		binary.LittleEndian.PutUint64(body[0:8], fh)
		binary.LittleEndian.PutUint64(body[8:16], offset)
		binary.LittleEndian.PutUint32(body[16:20], 256)
		reply := w.call(fuseOpReaddir, fuseRootID, body)
		w.wantErrno("readdir", reply, 0)
		var names []string
		for p := reply.payload; len(p) > 0; {
			nameLen := int(binary.LittleEndian.Uint32(p[16:20]))
			names = append(names, string(p[24:24+nameLen]))
			offset = binary.LittleEndian.Uint64(p[8:16])
			p = p[(24+nameLen+7)&^7:]
		}
		return names, offset
	}

	seen := map[string]int{}
	var offset uint64
	for {
		names, next := readBatch(offset)
		if len(names) == 0 {
			break
		}
		offset = next
		for _, name := range names {
			seen[name]++
			if name != "." && name != ".." {
				w.wantErrno("unlink "+name, w.call(fuseOpUnlink, fuseRootID, cName(name)), 0)
			}
		}
	}
	if len(seen) != total+2 {
		t.Fatalf("stream returned %d distinct entries, want %d", len(seen), total+2)
	}
	for name, n := range seen {
		if n != 1 {
			t.Fatalf("entry %q returned %d times", name, n)
		}
	}
	if names, _ := readBatch(0); len(names) != 2 {
		t.Fatalf("rewind must show the emptied directory, got %v", names)
	}
}

func TestStatfsReportsBackingStorage(t *testing.T) {
	w := newWireServer(t, ServerConfig{})
	r := w.call(fuseOpStatfs, fuseRootID, nil)
	w.wantErrno("statfs", r, 0)
	var st unix.Statfs_t
	if err := unix.Statfs(w.backing, &st); err != nil {
		t.Fatalf("statfs backing: %v", err)
	}
	if got := binary.LittleEndian.Uint64(r.payload[0:8]); got != st.Blocks {
		t.Fatalf("blocks %d, want backing %d", got, st.Blocks)
	}
	if got := binary.LittleEndian.Uint32(r.payload[48:52]); got != uint32(st.Frsize) {
		t.Fatalf("frsize %d, want backing %d", got, st.Frsize)
	}
}
