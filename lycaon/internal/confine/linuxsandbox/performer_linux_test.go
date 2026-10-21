//go:build linux

package linuxsandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

	result, _ = do(pathCall{Op: "chmod", Path: "x"}, dir)
	expect("unknown call", result, unix.ENOSYS, false)
	result, _, err := client.call(pathCall{Op: callMkdir, Path: "x", Dirs: 2}, []int{dir})
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
	if _, err := target.readPath(uint64(uintptr(unsafe.Pointer(&long[0])))); err != unix.ENAMETOOLONG {
		t.Fatalf("unterminated path: %v", err)
	}
	if _, err := target.readPath(0); err != unix.EFAULT {
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
	if _, err := target.openDir(-5); err != unix.EBADF {
		t.Fatalf("negative descriptor: %v", err)
	}
	var pipe [2]int
	testutil.FailErr(t, "pipe", unix.Pipe2(pipe[:], unix.O_CLOEXEC))
	_ = unix.Close(pipe[1])
	testutil.FailErr(t, "close a tracked descriptor", fdCloser(pipe[0]).Close())
	if err := fdCloser(pipe[0]).Close(); err != unix.EBADF {
		t.Fatalf("second close: %v", err)
	}
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
