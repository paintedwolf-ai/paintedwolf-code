//go:build linux

package linuxsandbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

// startTestPerformer serves the performer protocol in this process, on a
// thread of its own, for the floors given; the sandbox runs it in a process.
func startTestPerformer(t *testing.T, fs Filesystem) *performerClient {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "performer socket", err)
	floors, err := json.Marshal(fs)
	testutil.FailErr(t, "encode floors", err)
	testutil.FailErr(t, "send floors", unix.Sendmsg(pair[0], floors, nil, nil, 0))
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The thread keeps its own umask and is discarded with the goroutine.
		runtime.LockOSThread()
		if err := unix.Unshare(unix.CLONE_FS); err != nil {
			return
		}
		_ = servePathCalls(pair[1])
		_ = unix.Close(pair[1])
	}()
	t.Cleanup(func() { _ = unix.Close(pair[0]); <-done })
	return newPerformerClient(pair[0])
}

func openDirFD(t *testing.T, path string) int {
	t.Helper()
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "open "+path, err)
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd
}

func TestPerformerCreatesOrdinaryNamesAndRefusesProtectedOnes(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "fixture", os.MkdirAll(filepath.Join(root, "deep"), 0o755))
	testutil.FailErr(t, "fixture file", os.WriteFile(filepath.Join(root, "existing.txt"), []byte("x"), 0o644))
	client := startTestPerformer(t, Filesystem{
		WriteRoots:    []string{root},
		ReadOnly:      []string{filepath.Join(root, ".paintedwolf", "mcp.yaml"), filepath.Join(root, ".agents", "skills")},
		ReadOnlyNames: []NamedFloor{{Below: root, Name: "AGENTS.md"}},
	})
	dir := openDirFD(t, root)
	do := func(call pathCall, dirs ...int) (pathResult, int) {
		t.Helper()
		call.Dirs = len(dirs)
		result, opened, err := client.call(call, dirs)
		testutil.FailErr(t, "performer call "+call.Op+" "+call.Path, err)
		if opened >= 0 {
			t.Cleanup(func() { _ = unix.Close(opened) })
		}
		return result, opened
	}
	expect := func(name string, result pathResult, errno unix.Errno, refused bool) {
		t.Helper()
		if unix.Errno(result.Errno) != errno || (result.Refusal != nil) != refused {
			t.Fatalf("%s: errno %v refusal %+v, want %v refused=%v", name, unix.Errno(result.Errno), result.Refusal, errno, refused)
		}
	}

	result, opened := do(pathCall{Op: callOpen, Path: "created.txt", Flags: unix.O_CREAT | unix.O_WRONLY, Mode: 0o666, Umask: 0o077}, dir)
	expect("create ordinary", result, 0, false)
	if opened < 0 {
		t.Fatal("create returned no descriptor")
	}
	var st unix.Stat_t
	testutil.FailErr(t, "stat created", unix.Fstat(opened, &st))
	if st.Mode&0o777 != 0o600 {
		t.Fatalf("created mode %o, want the caller's umask applied", st.Mode&0o777)
	}
	result, _ = do(pathCall{Op: callOpen, Path: "deep/AGENTS.md", Flags: unix.O_CREAT | unix.O_WRONLY, Mode: 0o644}, dir)
	expect("create named floor", result, unix.EACCES, true)
	if result.Refusal.Operation != refusalCreate || result.Refusal.Target != filepath.Join(root, "deep", "AGENTS.md") {
		t.Fatalf("refusal %+v", result.Refusal)
	}
	result, _ = do(pathCall{Op: callOpen, Path: filepath.Join(root, "existing.txt"), Flags: unix.O_CREAT | unix.O_RDONLY}, dir)
	expect("open existing with O_CREAT", result, 0, false)
	result, _ = do(pathCall{Op: callOpen, Path: "existing.txt", Flags: unix.O_RDONLY}, dir)
	expect("openat2 that creates nothing", result, 0, false)
	result, _ = do(pathCall{Op: callOpen, Path: "deep/", Flags: unix.O_CREAT | unix.O_WRONLY}, dir)
	expect("create with a trailing slash", result, unix.EISDIR, false)

	result, _ = do(pathCall{Op: callSymlink, Path: "deep/pointer", Content: "AGENTS.md"}, dir)
	expect("symlink to a protected name", result, 0, false)
	result, _ = do(pathCall{Op: callOpen, Path: "deep/pointer", Flags: unix.O_CREAT | unix.O_WRONLY}, dir)
	expect("create through a symlink", result, unix.EACCES, true)
	result, _ = do(pathCall{Op: callOpen, Path: "deep/pointer", Flags: unix.O_CREAT | unix.O_WRONLY | unix.O_NOFOLLOW}, dir)
	expect("create through a symlink without following", result, unix.ELOOP, false)
	result, _ = do(pathCall{Op: callSymlink, Path: "deep/AGENTS.md", Content: "x"}, dir)
	expect("symlink named for a floor", result, unix.EACCES, true)

	result, _ = do(pathCall{Op: callMkdir, Path: ".paintedwolf", Mode: 0o755}, dir)
	expect("mkdir an ordinary directory", result, 0, false)
	result, _ = do(pathCall{Op: callMkdir, Path: ".", Mode: 0o755}, dir)
	expect("mkdir .", result, unix.EEXIST, false)
	result, _ = do(pathCall{Op: callMknod, Path: ".paintedwolf/mcp.yaml", Mode: unix.S_IFIFO | 0o644}, dir)
	expect("mknod a protected location", result, unix.EACCES, true)
	result, _ = do(pathCall{Op: callMknod, Path: "fifo", Mode: unix.S_IFIFO | 0o644}, dir)
	expect("mknod a fifo", result, 0, false)

	result, _ = do(pathCall{Op: callLink, Path: "existing.txt", NewPath: "deep/AGENTS.md"}, dir, dir)
	expect("link onto a floor", result, unix.EACCES, true)
	result, _ = do(pathCall{Op: callLink, Path: "existing.txt", NewPath: "hard.txt"}, dir, dir)
	expect("link", result, 0, false)
	result, _ = do(pathCall{Op: callRename, Path: "hard.txt", NewPath: ".paintedwolf/mcp.yaml"}, dir, dir)
	expect("rename onto a floor", result, unix.EACCES, true)
	result, _ = do(pathCall{Op: callRename, Path: "hard.txt", NewPath: "moved.txt"}, dir, dir)
	expect("rename", result, 0, false)
	result, _ = do(pathCall{Op: callRename, Path: "moved.txt", NewPath: ".", Flags: 0}, dir, dir)
	expect("rename onto .", result, unix.EBUSY, false)
	testutil.FailErr(t, "fixture agents dir", os.Mkdir(filepath.Join(root, ".agents"), 0o755))
	testutil.FailErr(t, "fixture stage", os.Mkdir(filepath.Join(root, "stage"), 0o755))
	result, _ = do(pathCall{Op: callRename, Path: "stage", NewPath: ".agents", Flags: unix.RENAME_EXCHANGE}, dir, dir)
	expect("exchange onto an ancestor of a floor", result, unix.EACCES, true)

	raw, err := encodePathCall(pathCall{Op: callMkdir, Path: "x", Notification: 9})
	testutil.FailErr(t, "frame", err)
	raw[3] = 1
	testutil.FailErr(t, "send malformed frame", unix.Sendmsg(client.fd, raw, nil, nil, 0))
	reply := make([]byte, 4096)
	n, _, _, _, err := unix.Recvmsg(client.fd, reply, nil, 0)
	testutil.FailErr(t, "malformed frame answer", err)
	result = pathResult{}
	testutil.FailErr(t, "decode answer", json.Unmarshal(reply[:n], &result))
	expect("malformed frame", result, unix.EINVAL, false)
	result, _, err = client.call(pathCall{Op: callMkdir, Path: "x", Dirs: 2}, []int{dir})
	testutil.FailErr(t, "mismatched call", err)
	expect("descriptor count mismatch", result, unix.EINVAL, false)
	for _, absent := range []string{"deep/AGENTS.md", ".paintedwolf/mcp.yaml"} {
		if _, err := os.Lstat(filepath.Join(root, absent)); err == nil {
			t.Fatalf("%s was created", absent)
		}
	}
}

func TestPerformerClientStopsAfterALostAnswer(t *testing.T) {
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "socket", err)
	defer func() { _ = unix.Close(pair[0]) }()
	_ = unix.Close(pair[1])
	client := &performerClient{fd: pair[0]}
	if _, _, err := client.call(pathCall{Op: callMkdir, Path: "x"}, nil); err == nil {
		t.Fatal("a closed performer answered")
	}
	if _, _, err := client.call(pathCall{Op: callMkdir, Path: "x"}, nil); err == nil {
		t.Fatal("a broken performer was asked again")
	}
}

func TestPathCallArgumentsFollowEachSyscall(t *testing.T) {
	t.Parallel()
	for nr, want := range map[uint32]pathArgs{
		unix.SYS_OPENAT:    {op: callOpen, dir: 0, path: 1, newDir: noArg, newPath: noArg, content: noArg, flags: 2, mode: 3, dev: noArg},
		unix.SYS_OPENAT2:   {op: callOpen, dir: 0, path: 1, newDir: noArg, newPath: noArg, content: noArg, flags: noArg, mode: noArg, dev: noArg, how: true},
		unix.SYS_MKDIRAT:   {op: callMkdir, dir: 0, path: 1, newDir: noArg, newPath: noArg, content: noArg, flags: noArg, mode: 2, dev: noArg},
		unix.SYS_MKNODAT:   {op: callMknod, dir: 0, path: 1, newDir: noArg, newPath: noArg, content: noArg, flags: noArg, mode: 2, dev: 3},
		unix.SYS_SYMLINKAT: {op: callSymlink, dir: 1, path: 2, newDir: noArg, newPath: noArg, content: 0, flags: noArg, mode: noArg, dev: noArg},
		unix.SYS_LINKAT:    {op: callLink, dir: 0, path: 1, newDir: 2, newPath: 3, content: noArg, flags: 4, mode: noArg, dev: noArg},
		unix.SYS_RENAMEAT:  {op: callRename, dir: 0, path: 1, newDir: 2, newPath: 3, content: noArg, flags: noArg, mode: noArg, dev: noArg},
		unix.SYS_RENAMEAT2: {op: callRename, dir: 0, path: 1, newDir: 2, newPath: 3, content: noArg, flags: 4, mode: noArg, dev: noArg},
	} {
		got, ok := pathArgsOf(nr)
		if !ok || got != want || !isPathCall(int32(nr)) {
			t.Errorf("syscall %d: %+v %v, want %+v", nr, got, ok, want)
		}
	}
	if _, ok := pathArgsOf(unix.SYS_UNLINKAT); ok || isPathCall(unix.SYS_UNLINKAT) {
		t.Error("unlinkat is not a name-creating call")
	}
}

func TestSelfRelativeLinksNameTheCaller(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/proc/self/fd/3":        "/proc/7/fd/3",
		"/proc/thread-self/fd/3": "/proc/7/task/9/fd/3",
		"/dev/fd/4":              "/proc/7/fd/4",
		"/dev/stdout":            "/proc/7/fd/1",
		"/proc/selfish":          "/proc/selfish",
		"relative/name":          "relative/name",
	} {
		if got := selfRelative(in, 7, 9); got != want {
			t.Errorf("selfRelative(%q) = %q, want %q", in, got, want)
		}
	}
	if got := selfRelative("/proc/self/fd/3", 0, 0); got != "/proc/self/fd/3" {
		t.Errorf("unknown caller rewrote %q", got)
	}
}

func TestTargetFactsReadTheCallersProcess(t *testing.T) {
	cache := newTargetHandles()
	handles, err := cache.acquire(os.Getpid())
	testutil.FailErr(t, "own handles", err)
	target := &targetProcess{pid: os.Getpid(), handles: handles, cache: cache}
	defer target.close()
	raw := append([]byte("some/relative/path"), 0)
	got, err := target.readPath(uint64(uintptr(unsafe.Pointer(&raw[0]))))
	testutil.FailErr(t, "read path", err)
	if got != "some/relative/path" {
		t.Fatalf("readPath = %q", got)
	}
	long := make([]byte, unix.PathMax+8)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := target.readPath(uint64(uintptr(unsafe.Pointer(&long[0])))); !errors.Is(err, unix.ENAMETOOLONG) {
		t.Fatalf("unterminated path: %v", err)
	}
	if _, err := target.readPath(0); !errors.Is(err, unix.EFAULT) {
		t.Fatalf("null path: %v", err)
	}
	runtime.KeepAlive(raw)
	runtime.KeepAlive(long)
	old := unix.Umask(0o027)
	defer unix.Umask(old)
	runtime.LockOSThread()
	facts, err := callerFactsOf(unix.Gettid())
	runtime.UnlockOSThread()
	testutil.FailErr(t, "caller facts", err)
	if facts.umask != 0o027 || facts.tgid != os.Getpid() || facts.nsPid != os.Getpid() || facts.nsTid <= 0 {
		t.Fatalf("caller facts %+v, want umask 027 and pid %d", facts, os.Getpid())
	}
	if gone, err := callerFactsOf(-1); err == nil || gone.umask != 0o777 {
		t.Fatalf("unreadable caller %+v %v, want an error and the most restrictive mask", gone, err)
	}
	cwd, err := target.openDir(unix.AT_FDCWD)
	testutil.FailErr(t, "open working directory", err)
	_ = unix.Close(cwd)
	held, err := unix.Open(t.TempDir(), unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	testutil.FailErr(t, "hold a directory", err)
	defer func() { _ = unix.Close(held) }()
	again, err := target.openDir(int32(held))
	testutil.FailErr(t, "open a held directory", err)
	_ = unix.Close(again)
	if _, err := target.openDir(-5); !errors.Is(err, unix.EBADF) {
		t.Fatalf("negative descriptor: %v", err)
	}
	var pipe [2]int
	testutil.FailErr(t, "pipe", unix.Pipe2(pipe[:], unix.O_CLOEXEC))
	_ = unix.Close(pipe[1])
	testutil.FailErr(t, "close a tracked descriptor", fdCloser(pipe[0]).Close())
	if err := fdCloser(pipe[0]).Close(); !errors.Is(err, unix.EBADF) {
		t.Fatalf("second close: %v", err)
	}
}

func TestAnswerKernelRetriesInterruptedAddFDBeforeSendingResult(t *testing.T) {
	if _, err := seccompIoctl(-1, unix.SECCOMP_IOCTL_NOTIF_ID_VALID, nil); err == 0 {
		t.Fatal("invalid listener descriptor succeeded")
	}
	orig := seccompIoctl
	t.Cleanup(func() { seccompIoctl = orig })

	var addCalls, sendCalls int
	var gotAdd seccompNotifAddfd
	var gotResp seccompNotifResp
	seccompIoctl = func(_ int, req uint, arg unsafe.Pointer) (uintptr, unix.Errno) {
		switch req {
		case unix.SECCOMP_IOCTL_NOTIF_ADDFD:
			addCalls++
			gotAdd = *(*seccompNotifAddfd)(arg)
			if addCalls == 1 {
				return 0, unix.EINTR
			}
			return 17, 0
		case unix.SECCOMP_IOCTL_NOTIF_SEND:
			sendCalls++
			gotResp = *(*seccompNotifResp)(arg)
			if sendCalls == 1 {
				return 0, unix.EINTR
			}
			return 0, 0
		default:
			return 0, unix.EINVAL
		}
	}

	answerKernel(9, pathCall{Notification: 42, Flags: unix.O_WRONLY | unix.O_CREAT | unix.O_CLOEXEC}, pathResult{}, 5)
	if addCalls != 2 || sendCalls != 2 {
		t.Fatalf("ioctl calls: add=%d send=%d, want 2 and 2", addCalls, sendCalls)
	}
	if gotAdd.ID != 42 || gotAdd.Flags != 0 || gotAdd.SrcFD != 5 || gotAdd.NewFDFlags != unix.O_CLOEXEC {
		t.Fatalf("addfd = %+v", gotAdd)
	}
	if gotResp.ID != 42 || gotResp.Val != 17 || gotResp.Error != 0 {
		t.Fatalf("resp = %+v, want val 17 and error 0", gotResp)
	}

	seccompIoctl = func(_ int, req uint, arg unsafe.Pointer) (uintptr, unix.Errno) {
		if req == unix.SECCOMP_IOCTL_NOTIF_ADDFD {
			return 0, unix.EMFILE
		}
		gotResp = *(*seccompNotifResp)(arg)
		return 0, unix.ENOENT
	}
	answerKernel(9, pathCall{Notification: 43}, pathResult{}, 6)
	if gotResp.ID != 43 || gotResp.Val != 0 || gotResp.Error != -int32(unix.EMFILE) {
		t.Fatalf("failed addfd resp = %+v, want -EMFILE", gotResp)
	}

	answerKernel(9, pathCall{Notification: 44}, pathResult{Errno: int(unix.EACCES)}, -1)
	if gotResp.ID != 44 || gotResp.Val != 0 || gotResp.Error != -int32(unix.EACCES) {
		t.Fatalf("refusal resp = %+v, want -EACCES", gotResp)
	}
}

func TestPerformerDisconnectRejectsListenerAndForwardedCalls(t *testing.T) {
	for _, operation := range []string{"lend", "forward"} {
		t.Run(operation, func(t *testing.T) {
			pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
			testutil.FailErr(t, "disconnected performer socket", err)
			_ = unix.Close(pair[1])
			client := newPerformerClient(pair[0])
			defer client.Close()
			if operation == "lend" {
				err = client.lend(pair[0], func(pathResult) {})
			} else {
				client.forwarding, client.listener = true, -1
				client.pending = make(map[uint64]struct{})
				err = client.forward(pathCall{Op: callMkdir, Path: "created", Notification: 1}, nil)
			}
			if err == nil || !client.broken || len(client.pending) != 0 {
				t.Fatalf("disconnected %s: error=%v broken=%v pending=%v", operation, err, client.broken, client.pending)
			}
		})
	}
}

func TestForwardedCallsAreRefusedWhenThePerformerStops(t *testing.T) {
	for _, end := range []string{"closed", "malformed"} {
		t.Run(end, func(t *testing.T) {
			client, peer, finish := forwardingTestClient(t, func(pathResult) {})
			defer finish()
			orig := seccompIoctl
			t.Cleanup(func() { seccompIoctl = orig })
			var mu sync.Mutex
			var answered []seccompNotifResp
			seccompIoctl = func(_ int, req uint, arg unsafe.Pointer) (uintptr, unix.Errno) {
				mu.Lock()
				defer mu.Unlock()
				if req == unix.SECCOMP_IOCTL_NOTIF_ID_VALID {
					if *(*uint64)(arg) == 3 {
						return 0, unix.ENOENT
					}
					return 0, 0
				}
				if req == unix.SECCOMP_IOCTL_NOTIF_SEND {
					answered = append(answered, *(*seccompNotifResp)(arg))
					return 0, 0
				}
				return 0, unix.EINVAL
			}
			for id := uint64(1); id <= 3; id++ {
				testutil.FailErr(t, "forward call", client.forward(pathCall{Op: callMkdir, Path: "created", Notification: id}, nil))
				receiveForwardedCall(t, peer)
			}
			testutil.FailErr(t, "acknowledge completed call", unix.Sendmsg(peer, []byte(`{"notification":1}`), nil, nil, 0))
			testutil.WaitFor(t, func() bool {
				client.mu.Lock()
				defer client.mu.Unlock()
				_, pending := client.pending[1]
				return !pending
			})
			if end == "closed" {
				_ = unix.Shutdown(peer, unix.SHUT_RDWR)
			} else {
				testutil.FailErr(t, "malformed acknowledgment", unix.Sendmsg(peer, []byte(`{`), nil, nil, 0))
			}
			testutil.WaitFor(t, func() bool {
				client.mu.Lock()
				defer client.mu.Unlock()
				return client.broken && len(client.pending) == 0
			})
			mu.Lock()
			defer mu.Unlock()
			if len(answered) != 1 || answered[0].ID != 2 || answered[0].Error != -int32(unix.EACCES) {
				t.Fatalf("pending answers = %+v, want only live call 2 refused", answered)
			}
			if err := client.forward(pathCall{Op: callMkdir, Notification: 4}, nil); err == nil {
				t.Fatal("forwarded another call to a stopped performer")
			}
		})
	}
}

func TestForwardedAcknowledgmentsDrainWhileSending(t *testing.T) {
	client, peer, finish := forwardingTestClient(t, func(pathResult) {})
	defer finish()
	testutil.FailErr(t, "bound request buffer", unix.SetsockoptInt(client.fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 1024))
	testutil.FailErr(t, "bound response buffer", unix.SetsockoptInt(peer, unix.SOL_SOCKET, unix.SO_SNDBUF, 1024))
	const calls = 256
	var senders sync.WaitGroup
	errors := make(chan error, calls)
	for id := uint64(1); id <= calls; id++ {
		senders.Go(func() { errors <- client.forward(pathCall{Op: callMkdir, Path: "created", Notification: id}, nil) })
	}
	for range calls {
		call := receiveForwardedCall(t, peer)
		reply, err := json.Marshal(pathResult{Notification: call.Notification})
		testutil.FailErr(t, "encode acknowledgment", err)
		testutil.FailErr(t, "acknowledge call", unix.Sendmsg(peer, reply, nil, nil, 0))
	}
	senders.Wait()
	close(errors)
	for err := range errors {
		testutil.FailErr(t, "forward call under backpressure", err)
	}
	testutil.WaitFor(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return len(client.pending) == 0
	})
}

func TestPerformerCloseWaitsForRefusalReporting(t *testing.T) {
	started, release, reported := make(chan struct{}, 1), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	client, peer, finish := forwardingTestClient(t, func(pathResult) { started <- struct{}{}; <-release; close(reported) })
	defer finish()
	defer releaseOnce.Do(func() { close(release) })
	testutil.FailErr(t, "forward refused call", client.forward(pathCall{Op: callMkdir, Notification: 1}, nil))
	receiveForwardedCall(t, peer)
	testutil.FailErr(t, "report refusal", unix.Sendmsg(peer, []byte(`{"notification":1,"refusal":{"unexamined":true}}`), nil, nil, 0))
	testutil.Receive(t, "start refusal report", started)
	closed := make(chan struct{}, 1)
	go func() { finish(); closed <- struct{}{} }()
	buf := make([]byte, 1)
	n, _, _, _, err := unix.Recvmsg(peer, buf, nil, 0)
	testutil.FailErr(t, "observe client shutdown", err)
	if n != 0 {
		t.Fatalf("shutdown returned %d bytes, want EOF", n)
	}
	select {
	case <-closed:
		t.Fatal("client closed while refusal reporting was blocked")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	testutil.Receive(t, "join client close", closed)
	select {
	case <-reported:
	default:
		t.Fatal("client close returned before refusal reporting completed")
	}
}

func TestPerformerCloseJoinsABlockedForward(t *testing.T) {
	client, _, finish := forwardingTestClient(t, func(pathResult) {})
	defer finish()
	testutil.FailErr(t, "bound request buffer", unix.SetsockoptInt(client.fd, unix.SOL_SOCKET, unix.SO_SNDBUF, 1024))
	raw, err := encodePathCall(pathCall{Op: callMkdir, Path: "queued"})
	testutil.FailErr(t, "encode queued call", err)
	for {
		err = unix.Sendmsg(client.fd, raw, nil, nil, unix.MSG_DONTWAIT)
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		testutil.FailErr(t, "fill request buffer", err)
	}
	forwarded := make(chan error, 1)
	go func() { forwarded <- client.forward(pathCall{Op: callMkdir, Notification: 2}, nil) }()
	testutil.WaitFor(t, func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		_, pending := client.pending[2]
		return pending
	})
	finish()
	if !client.sendMu.TryLock() {
		t.Fatal("client closed while a forward still held its descriptor")
	}
	client.sendMu.Unlock()
	if err := testutil.Receive(t, "join blocked forward", forwarded); err == nil {
		t.Fatal("blocked forward succeeded after shutdown")
	}
	if len(client.pending) != 0 {
		t.Fatalf("pending after close = %v", client.pending)
	}
}

func forwardingTestClient(t *testing.T, report func(pathResult)) (*performerClient, int, func()) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "forwarding socket", err)
	listener, err := unix.Eventfd(0, unix.EFD_CLOEXEC)
	testutil.FailErr(t, "listener fixture", err)
	client := newPerformerClient(pair[0])
	testutil.FailErr(t, "lend listener", client.lend(listener, report))
	receiveForwardedCall(t, pair[1])
	var closeOnce sync.Once
	finish := func() { closeOnce.Do(func() { testutil.FailErr(t, "close forwarding client", client.Close()) }) }
	t.Cleanup(func() {
		_ = unix.Close(pair[1])
		_ = unix.Close(listener)
	})
	return client, pair[1], finish
}

func receiveForwardedCall(t *testing.T, fd int) pathCall {
	t.Helper()
	buf, oob := make([]byte, maxPathMessage), make([]byte, unix.CmsgSpace(4))
	n, oobn, _, _, err := unix.Recvmsg(fd, buf, oob, unix.MSG_CMSG_CLOEXEC)
	testutil.FailErr(t, "receive forwarded call", err)
	for _, received := range receivedFDs(oob[:oobn]) {
		_ = unix.Close(received)
	}
	call, err := decodePathCall(buf[:n])
	testutil.FailErr(t, "decode forwarded call", err)
	return call
}

// BenchmarkPerformerCreate measures one create through the performer's
// protocol, without the seccomp notification around it.
func BenchmarkPerformerCreate(b *testing.B) {
	root := b.TempDir()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		b.Fatalf("socket: %v", err)
	}
	floors, _ := json.Marshal(Filesystem{WriteRoots: []string{root}, ReadOnlyNames: []NamedFloor{{Below: root, Name: "AGENTS.md"}}})
	_ = unix.Sendmsg(pair[0], floors, nil, nil, 0)
	go func() {
		runtime.LockOSThread()
		if unix.Unshare(unix.CLONE_FS) == nil {
			_ = servePathCalls(pair[1])
		}
	}()
	defer func() { _ = unix.Close(pair[0]) }()
	client := newPerformerClient(pair[0])
	dir, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		b.Fatalf("root: %v", err)
	}
	defer func() { _ = unix.Close(dir) }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, opened, err := client.call(pathCall{Op: callOpen, Path: "f" + strconv.Itoa(i), Flags: unix.O_CREAT | unix.O_WRONLY, Mode: 0o644, Dirs: 1}, []int{dir})
		if err != nil || result.Errno != 0 || opened < 0 {
			b.Fatalf("create: %+v %v", result, err)
		}
		_ = unix.Close(opened)
	}
}
