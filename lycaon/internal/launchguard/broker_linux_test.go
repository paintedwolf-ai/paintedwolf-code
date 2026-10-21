//go:build linux

package launchguard

import (
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/launchbroker"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestBrokerLooksProgramsUpOnTheRequestPath(t *testing.T) {
	path, err := lookPath("sh", []string{"PATH=relative:/usr/bin:/bin"})
	testutil.FailErr(t, "resolve sh", err)
	if !strings.HasSuffix(path, "/sh") || !strings.HasPrefix(path, "/") {
		t.Fatalf("resolved %q", path)
	}
	if _, err := lookPath("sh", []string{"PATH=relative"}); err == nil {
		t.Fatal("a relative PATH entry resolved a program")
	}
}

// A brokered program receives exactly the request's descriptors at 0, 1, 2
// and 3.., and a path naming a descriptor executes that very file: the desktop
// runs a manifest-verified image as "/proc/self/fd/3" with no path lookup.
func TestBrokeredProgramsExecutePassedDescriptorsInPlace(t *testing.T) {
	testutil.FailErr(t, "close-on-exec inherited descriptors", unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC))
	sh, err := lookPath("sh", []string{"PATH=/usr/bin:/bin"})
	testutil.FailErr(t, "resolve sh", err)
	image, err := os.Open(sh)
	testutil.FailErr(t, "open image", err)
	defer image.Close()
	null, err := os.Open("/dev/null")
	testutil.FailErr(t, "open stdin", err)
	defer null.Close()
	read, write, err := os.Pipe()
	testutil.FailErr(t, "pipe", err)
	defer read.Close()
	extra, err := os.CreateTemp(t.TempDir(), "extra")
	testutil.FailErr(t, "extra descriptor", err)
	defer extra.Close()
	request := launchbroker.Request{
		Version: 1,
		Argv:    []string{"/proc/self/fd/3", "-c", "readlink /proc/self/fd/4; ls /proc/self/fd | sort -n | tr '\\n' ' '"},
		Dir:     "/", Environment: []string{"PATH=/usr/bin:/bin"},
	}
	// The first descriptor is the call channel, which the program never sees.
	pid, err := startBrokered(request, []int{-1, int(null.Fd()), int(write.Fd()), int(write.Fd()), int(image.Fd()), int(extra.Fd())})
	testutil.FailErr(t, "start brokered program", err)
	_ = write.Close()
	output, _ := io.ReadAll(read)
	var status syscall.WaitStatus
	_, err = syscall.Wait4(pid, &status, 0, nil)
	testutil.FailErr(t, "wait", err)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if status.ExitStatus() != 0 || len(lines) != 2 || lines[0] != extra.Name() {
		t.Fatalf("brokered program: status %v, output %q", status, output)
	}
	// ls holds one directory descriptor of its own beyond the passed ones.
	if fds := strings.Fields(lines[1]); len(fds) > 6 || fds[0] != "0" || fds[4] != "4" {
		t.Fatalf("brokered program saw descriptors %v, want 0..4 and ls's own", fds)
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "call channel", err)
	channel := os.NewFile(uintptr(pair[0]), "call-channel")
	defer channel.Close()
	inNull, err := os.Open("/dev/null")
	testutil.FailErr(t, "open null", err)
	agent := &brokerAgent{calls: map[int]*brokerCall{}}
	agent.start(launchbroker.Request{
		Version:     1,
		Argv:        []string{sh, "-c", "exit 0"},
		Dir:         "/",
		Environment: []string{"PATH=/usr/bin:/bin"},
	}, []int{pair[1], int(inNull.Fd()), int(inNull.Fd()), int(inNull.Fd())})
	started, err := launchbroker.ReadEvent(channel)
	testutil.FailErr(t, "read started event", err)
	if started.Started <= 0 {
		t.Fatalf("started event = %+v", started)
	}
	_, _ = syscall.Wait4(started.Started, &status, 0, nil)
	agent.finished(started.Started, unix.WaitStatus(status))
}
