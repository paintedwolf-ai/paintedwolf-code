//go:build linux

package linuxsandbox

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func streamFile(t *testing.T, content string) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stream-")
	testutil.FailErr(t, "create stream fixture", err)
	t.Cleanup(func() { _ = f.Close() })
	_, err = f.WriteString(content)
	testutil.FailErr(t, "seed stream fixture", err)
	_, err = f.Seek(0, io.SeekStart)
	testutil.FailErr(t, "rewind stream fixture", err)
	return f
}

func TestStandardStreamsRetainObjectsAndJoinCopies(t *testing.T) {
	payload := strings.Repeat("retained bytes", 8192)
	source := streamFile(t, payload)
	original := source.Name()
	renamed := original + "-retained"
	testutil.FailErr(t, "rename source fixture", os.Rename(original, renamed))
	testutil.FailErr(t, "plant pathname replacement", os.WriteFile(original, []byte("decoy"), 0o600))
	streams, err := newStandardStreams([3]*os.File{source, nil, nil}, sameOpenFile)
	testutil.FailErr(t, "prepare byte input", err)
	t.Cleanup(func() { _ = streams.finish() })
	raw, err := io.ReadAll(streams.files[0])
	testutil.FailErr(t, "read byte input through EOF", err)
	if string(raw) != payload {
		t.Fatalf("read %q", raw)
	}
	testutil.FailErr(t, "settle byte input", streams.finish())
	for _, bridge := range streams.bridges {
		select {
		case <-bridge.done:
		default:
			t.Fatal("copy survived settlement")
		}
		var st unix.Stat_t
		if err := unix.Fstat(int(bridge.original.Fd()), &st); !errors.Is(err, unix.EBADF) {
			t.Fatal("retained source descriptor survived settlement")
		}
	}
	data, err := os.ReadFile(original)
	testutil.FailErr(t, "read untouched replacement", err)
	if string(data) != "decoy" {
		t.Fatal("copy changed the pathname replacement")
	}
}

func TestStandardStreamsBoundReadAheadAndCancelEarlyInput(t *testing.T) {
	source := streamFile(t, "")
	testutil.FailErr(t, "size sparse input fixture", source.Truncate(64<<20))
	streams, err := newStandardStreams([3]*os.File{source, nil, nil}, sameOpenFile)
	testutil.FailErr(t, "prepare large byte input", err)
	t.Cleanup(func() { _ = streams.finish() })
	capacity, err := unix.FcntlInt(streams.files[0].Fd(), unix.F_GETPIPE_SZ, 0)
	testutil.FailErr(t, "inspect pipe capacity", err)
	// One byte consumed proves the producer entered the real copy loop.
	first := make([]byte, 1)
	_, err = streams.files[0].Read(first)
	testutil.FailErr(t, "read initial byte", err)
	testutil.FailErr(t, "cancel early input and join", streams.finish())
	offset, err := source.Seek(0, io.SeekCurrent)
	testutil.FailErr(t, "inspect shared input offset", err)
	if offset > int64(capacity+2*32*1024) {
		t.Fatalf("read ahead %d beyond pipe plus fixed blocks", offset)
	}
}

func TestStandardStreamsPreserveSharedAndSeparateOutputOffsets(t *testing.T) {
	t.Run("shared description", func(t *testing.T) {
		output := streamFile(t, "")
		fd, err := unix.FcntlInt(output.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		testutil.FailErr(t, "duplicate output description", err)
		duplicate := os.NewFile(uintptr(fd), "shared-output")
		defer duplicate.Close()
		streams, err := newStandardStreams([3]*os.File{nil, output, duplicate}, sameOpenFile)
		testutil.FailErr(t, "prepare shared output", err)
		t.Cleanup(func() { _ = streams.finish() })
		if streams.files[1] != streams.files[2] {
			t.Fatal("shared output lost its one pipe")
		}
		for _, chunk := range []struct {
			stream int
			text   string
		}{{1, "one"}, {2, "two"}, {1, "three"}, {2, "four"}} {
			_, err = streams.files[chunk.stream].WriteString(chunk.text)
			testutil.FailErr(t, "write ordered output", err)
		}
		testutil.FailErr(t, "drain shared output", streams.finish())
		data, err := os.ReadFile(output.Name())
		testutil.FailErr(t, "read ordered shared output", err)
		if string(data) != "onetwothreefour" {
			t.Fatalf("shared output=%q", data)
		}
	})
	t.Run("separate descriptions same inode", func(t *testing.T) {
		output := streamFile(t, "-------")
		second, err := os.OpenFile(output.Name(), os.O_WRONLY, 0)
		testutil.FailErr(t, "open separate output description", err)
		defer second.Close()
		_, err = second.Seek(4, io.SeekStart)
		testutil.FailErr(t, "position independent output", err)
		streams, err := newStandardStreams([3]*os.File{nil, output, second}, sameOpenFile)
		testutil.FailErr(t, "prepare independent output", err)
		t.Cleanup(func() { _ = streams.finish() })
		if streams.files[1] == streams.files[2] {
			t.Fatal("independent file descriptions merged")
		}
		_, err = streams.files[1].WriteString("AAA")
		testutil.FailErr(t, "write first output", err)
		_, err = streams.files[2].WriteString("BBB")
		testutil.FailErr(t, "write second output", err)
		testutil.FailErr(t, "drain separate outputs", streams.finish())
		data, err := os.ReadFile(output.Name())
		testutil.FailErr(t, "read independent output offsets", err)
		if string(data) != "AAA-BBB" {
			t.Fatalf("independent outputs=%q", data)
		}
	})
}

func TestStandardStreamsPassPipeSocketDeviceAndClosedDescriptors(t *testing.T) {
	read, write, err := os.Pipe()
	testutil.FailErr(t, "create passthrough pipe", err)
	defer read.Close()
	defer write.Close()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "create passthrough socket", err)
	socket := os.NewFile(uintptr(pair[0]), "socket")
	defer socket.Close()
	defer unix.Close(pair[1])
	device, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	testutil.FailErr(t, "open passthrough device", err)
	defer device.Close()
	for _, file := range []*os.File{read, socket, device} {
		streams, err := newStandardStreams([3]*os.File{file, nil, nil}, sameOpenFile)
		testutil.FailErr(t, "prepare passthrough descriptors", err)
		if len(streams.bridges) != 0 || streams.files[0] != file {
			t.Fatal("nonregular stream changed")
		}
		if streams.files[1].Fd() != ^uintptr(0) || streams.files[2].Fd() != ^uintptr(0) {
			t.Fatal("closed stream was replaced by an open object")
		}
		testutil.FailErr(t, "settle passthrough descriptors", streams.finish())
	}
}

func TestStandardStreamsReportInitializationAndCopyFailures(t *testing.T) {
	t.Run("invalid access", func(t *testing.T) {
		source := streamFile(t, "read only")
		readOnly, err := os.Open(source.Name())
		testutil.FailErr(t, "open read-only output fixture", err)
		defer readOnly.Close()
		_, err = newStandardStreams([3]*os.File{nil, readOnly, nil}, sameOpenFile)
		var failure *StreamError
		if !errors.As(err, &failure) || failure.Stream != 1 || failure.Operation != "access" || !errors.Is(err, unix.EBADF) {
			t.Fatalf("wrong initialization failure: %v", err)
		}
	})
	t.Run("real write failure", func(t *testing.T) {
		fd, err := unix.MemfdCreate("sealed-output", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
		testutil.FailErr(t, "create sealed output fixture", err)
		output := os.NewFile(uintptr(fd), "sealed-output")
		defer output.Close()
		_, err = unix.FcntlInt(output.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_WRITE)
		testutil.FailErr(t, "seal output fixture", err)
		streams, err := newStandardStreams([3]*os.File{nil, output, nil}, sameOpenFile)
		testutil.FailErr(t, "prepare sealed byte output", err)
		_, err = streams.files[1].Write([]byte("cannot arrive"))
		testutil.FailErr(t, "submit output bytes", err)
		<-streams.bridges[0].done
		err = streams.finish()
		var failure *StreamError
		if !errors.As(err, &failure) || failure.Stream != 1 || failure.Operation != "copy" || !errors.Is(err, unix.EPERM) {
			t.Fatalf("wrong copy failure: %v", err)
		}
	})
}

func TestCopyStreamKeepsPartialReadsAndReportsShortWrites(t *testing.T) {
	var out bytes.Buffer
	testutil.FailErr(t, "copy bounded bytes", copyStream(&out, bytes.NewBufferString("all bytes"), make([]byte, 3), func() bool { return false }))
	if out.String() != "all bytes" {
		t.Fatal("copy lost a partial block")
	}
	if err := copyStream(zeroStreamWriter{}, bytes.NewBufferString("x"), make([]byte, 1), func() bool { return false }); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write=%v", err)
	}
}

type zeroStreamWriter struct{}

func (zeroStreamWriter) Write([]byte) (int, error) { return 0, nil }

func TestStandardStreamsRefuseAmbiguousOutputsWithoutKernelComparison(t *testing.T) {
	output := streamFile(t, "original")
	_, err := newStandardStreams([3]*os.File{nil, output, output}, func(*os.File, *os.File) (bool, error) { return false, unix.ENOSYS })
	var failure *StreamError
	if !errors.As(err, &failure) || failure.Stream != 2 || failure.Operation != "compare shared output" || !errors.Is(err, unix.ENOSYS) {
		t.Fatalf("ambiguous output failure=%v", err)
	}
	second := streamFile(t, "")
	streams, err := newStandardStreams([3]*os.File{nil, output, second}, func(*os.File, *os.File) (bool, error) {
		t.Fatal("distinct files need no kernel comparison")
		return false, nil
	})
	testutil.FailErr(t, "prepare ordinary distinct output files", err)
	testutil.FailErr(t, "settle distinct outputs", streams.finish())
}

func TestStandardStreamsDrainQueuedBytesWithoutWaitingForAnotherWriter(t *testing.T) {
	output := streamFile(t, "")
	streams, err := newStandardStreams([3]*os.File{nil, output, output}, sameOpenFile)
	testutil.FailErr(t, "prepare retained-writer fixture", err)
	fd, err := unix.FcntlInt(streams.files[1].Fd(), unix.F_DUPFD_CLOEXEC, 3)
	testutil.FailErr(t, "retain another output writer", err)
	other := os.NewFile(uintptr(fd), "retained-writer")
	defer other.Close()
	for _, chunk := range []struct {
		stream int
		text   string
	}{{1, "queued-one"}, {2, "queued-two"}} {
		_, err = streams.files[chunk.stream].WriteString(chunk.text)
		testutil.FailErr(t, "queue target output", err)
	}
	testutil.FailErr(t, "settle output with another writer still open", streams.finish())
	data, err := os.ReadFile(output.Name())
	testutil.FailErr(t, "read all settled target output", err)
	if string(data) != "queued-onequeued-two" {
		t.Fatalf("settlement lost queued target bytes: %q", data)
	}
	if _, err := other.WriteString("after-settlement"); !errors.Is(err, unix.EPIPE) {
		t.Fatalf("retained writer still reaches output after settlement: %v", err)
	}
}

func TestStandardStreamsRefuseDirectoryMetadataAuthority(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	testutil.FailErr(t, "open owned directory fixture", err)
	defer directory.Close()
	_, err = newStandardStreams([3]*os.File{directory, nil, nil}, sameOpenFile)
	var failure *StreamError
	if !errors.As(err, &failure) || failure.Stream != 0 || failure.Operation != "unsupported descriptor kind" {
		t.Fatalf("directory descriptor entered a byte stream: %v", err)
	}
}
