//go:build linux

package launchguard

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

type streamPolicyObservation struct {
	Before   [3]unix.Stat_t
	After    [3]unix.Stat_t
	Flags    [3]int
	Replaced []standardStreamReplacement
	Error    string
}

func TestStandardStreamPolicyChild(t *testing.T) {
	if os.Getenv("PW_STREAM_POLICY_CHILD") != "1" {
		return
	}
	var observation streamPolicyObservation
	for fd := range observation.Before {
		if err := unix.Fstat(fd, &observation.Before[fd]); err != nil {
			os.Exit(2)
		}
	}
	replaced, err := sanitizeStandardStreams()
	observation.Replaced = replaced
	if err != nil {
		observation.Error = err.Error()
	}
	for fd := range observation.After {
		if err := unix.Fstat(fd, &observation.After[fd]); err != nil {
			os.Exit(3)
		}
		observation.Flags[fd], err = unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
		if err != nil {
			os.Exit(4)
		}
	}
	report := os.NewFile(3, "policy-observation")
	if json.NewEncoder(report).Encode(observation) != nil {
		os.Exit(5)
	}
	os.Exit(0)
}

func TestStandardStreamsReplaceMappableFiles(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "mappable")
	testutil.FailErr(t, "create regular stream", err)
	defer func() { _ = file.Close() }()
	observation := observeStreamPolicy(t, [3]*os.File{file, file, file})
	if len(observation.Replaced) != 3 {
		t.Fatalf("replacement facts: %+v", observation.Replaced)
	}
	for fd, actual := range observation.After {
		if actual.Mode&unix.S_IFMT != unix.S_IFCHR || actual.Rdev != unix.Mkdev(1, 3) || actual.Uid != 0 {
			t.Fatalf("fd %d replacement: %+v", fd, actual)
		}
		access := unix.O_WRONLY
		if fd == 0 {
			access = unix.O_RDONLY
		}
		if observation.Flags[fd]&unix.O_ACCMODE != access {
			t.Fatalf("fd %d access: %d", fd, observation.Flags[fd])
		}
		fact := observation.Replaced[fd]
		if fact.Descriptor != fd || fact.FileType != unix.S_IFREG || fact.Event != "protected_standard_stream_replaced" {
			t.Fatalf("fd %d fact: %+v", fd, fact)
		}
	}
}

func TestStandardStreamsPreserveTransports(t *testing.T) {
	t.Run("pipe", func(t *testing.T) {
		read, write, err := os.Pipe()
		testutil.FailErr(t, "create pipe", err)
		defer func() { _ = read.Close(); _ = write.Close() }()
		assertStreamTransports(t, [3]*os.File{read, write, write})
	})
	t.Run("socket", func(t *testing.T) {
		pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		testutil.FailErr(t, "create socket pair", err)
		first, second := os.NewFile(uintptr(pair[0]), "socket"), os.NewFile(uintptr(pair[1]), "peer")
		defer func() { _ = first.Close(); _ = second.Close() }()
		assertStreamTransports(t, [3]*os.File{first, first, first})
	})
	t.Run("terminal", func(t *testing.T) {
		terminal, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
		testutil.FailErr(t, "open owned terminal multiplexer", err)
		defer func() { _ = terminal.Close() }()
		_, err = unix.IoctlGetTermios(int(terminal.Fd()), unix.TCGETS)
		testutil.FailErr(t, "verify terminal contract", err)
		assertStreamTransports(t, [3]*os.File{terminal, terminal, terminal})
	})
}

func assertStreamTransports(t *testing.T, streams [3]*os.File) {
	t.Helper()
	observation := observeStreamPolicy(t, streams)
	if len(observation.Replaced) != 0 {
		t.Fatalf("supported transport replaced: %+v", observation.Replaced)
	}
	for fd, before := range observation.Before {
		after := observation.After[fd]
		if before.Dev != after.Dev || before.Ino != after.Ino || before.Rdev != after.Rdev || before.Mode != after.Mode {
			t.Fatalf("fd %d transport changed: before=%+v after=%+v", fd, before, after)
		}
		flags, err := unix.FcntlInt(streams[fd].Fd(), unix.F_GETFL, 0)
		testutil.FailErr(t, "read original access flags", err)
		if observation.Flags[fd] != flags {
			t.Fatalf("fd %d flags changed: %d -> %d", fd, flags, observation.Flags[fd])
		}
	}
}

func observeStreamPolicy(t *testing.T, streams [3]*os.File) streamPolicyObservation {
	t.Helper()
	report, err := os.CreateTemp(t.TempDir(), "observation")
	testutil.FailErr(t, "create owned observation", err)
	defer func() { _ = report.Close() }()
	executable, err := os.Executable()
	testutil.FailErr(t, "select current test image", err)
	command := exec.Command(executable, "-test.run=^TestStandardStreamPolicyChild$")
	command.Env = append(os.Environ(), "PW_STREAM_POLICY_CHILD=1")
	command.Stdin, command.Stdout, command.Stderr = streams[0], streams[1], streams[2]
	command.ExtraFiles = []*os.File{report}
	testutil.FailErr(t, "run isolated stream policy", command.Run())
	_, err = report.Seek(0, 0)
	testutil.FailErr(t, "rewind observation", err)
	var observation streamPolicyObservation
	testutil.FailErr(t, "decode stream facts", json.NewDecoder(report).Decode(&observation))
	if observation.Error != "" {
		t.Fatalf("stream policy: %s", observation.Error)
	}
	return observation
}
