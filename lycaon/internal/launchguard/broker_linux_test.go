//go:build linux

package launchguard

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
}

func startTestBrokerAgent(t *testing.T) (*brokerAgent, *os.File, <-chan struct{}) {
	t.Helper()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	testutil.FailErr(t, "broker socketpair", err)
	server := os.NewFile(uintptr(pair[0]), "broker-server")
	client := os.NewFile(uintptr(pair[1]), "broker-client")
	agent := &brokerAgent{calls: map[int]*brokerCall{}}
	done := make(chan struct{})
	go func() {
		agent.serve(server)
		close(done)
	}()
	t.Cleanup(func() {
		// Closing the client ends the receive loop, which closes the server
		// itself; closing it here would race the loop's descriptor reads.
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = server.Close()
		}
	})
	return agent, client, done
}

func TestBrokerOrdersStartedBeforeExitedForImmediateExitChild(t *testing.T) {
	_, client, done := startTestBrokerAgent(t)
	null, err := os.Open("/dev/null")
	testutil.FailErr(t, "open /dev/null", err)
	defer func() { _ = null.Close() }()

	for i := 0; i < 25; i++ {
		channel, err := launchbroker.Send(client, launchbroker.Request{
			Argv:        []string{"true"},
			Environment: []string{"PATH=/usr/bin:/bin"},
			Dir:         "/",
		}, null, null, null)
		testutil.FailErr(t, "send immediate-exit request", err)
		first, err := launchbroker.ReadEvent(channel)
		testutil.FailErr(t, "read first event", err)
		if first.Started <= 0 || first.Exited || first.Error != "" {
			t.Fatalf("iteration %d: first event = %+v, want Started > 0 before Exited", i, first)
		}
		second, err := launchbroker.ReadEvent(channel)
		testutil.FailErr(t, "read second event", err)
		if !second.Exited || second.Code != 0 || second.Error != "" {
			t.Fatalf("iteration %d: second event = %+v, want Exited with code 0", i, second)
		}
		_ = channel.Close()
	}
	testutil.FailErr(t, "close client", client.Close())
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("broker agent did not exit after client closed")
	}
}

func TestBrokerBoundsConcurrentActiveCalls(t *testing.T) {
	agent, client, _ := startTestBrokerAgent(t)
	null, err := os.Open("/dev/null")
	testutil.FailErr(t, "open /dev/null", err)
	defer func() { _ = null.Close() }()

	agent.mu.Lock()
	for pid := 1; pid <= maxConcurrentBrokerCalls; pid++ {
		agent.calls[pid] = &brokerCall{channel: -1, detach: true}
	}
	agent.mu.Unlock()
	defer func() {
		agent.mu.Lock()
		clear(agent.calls)
		agent.mu.Unlock()
	}()

	channel, err := launchbroker.Send(client, launchbroker.Request{
		Argv:        []string{"true"},
		Environment: []string{"PATH=/usr/bin:/bin"},
		Dir:         "/",
	}, null, null, null)
	testutil.FailErr(t, "send request at cap", err)
	defer func() { _ = channel.Close() }()
	event, err := launchbroker.ReadEvent(channel)
	testutil.FailErr(t, "read cap event", err)
	if !strings.Contains(event.Error, "broker active call limit reached") {
		t.Fatalf("event = %+v, want broker active call limit reached", event)
	}
}

func TestBrokerDetachedCallDoesNotBlockShutdown(t *testing.T) {
	_, client, done := startTestBrokerAgent(t)
	null, err := os.Open("/dev/null")
	testutil.FailErr(t, "open /dev/null", err)
	defer func() { _ = null.Close() }()

	readPipe, writePipe, err := os.Pipe()
	testutil.FailErr(t, "pipe", err)
	channel, err := launchbroker.Send(client, launchbroker.Request{
		Argv:        []string{"sh", "-c", "sleep 60 </dev/null >/dev/null 2>&1 & echo $!"},
		Environment: []string{"PATH=/usr/bin:/bin"},
		Dir:         "/",
		Detach:      true,
	}, null, writePipe, null)
	_ = writePipe.Close()
	testutil.FailErr(t, "send detached request", err)
	out, err := io.ReadAll(readPipe)
	_ = readPipe.Close()
	testutil.FailErr(t, "read detached pid", err)
	bgPID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	testutil.FailErr(t, "parse detached pid", err)
	t.Cleanup(func() { _ = syscall.Kill(bgPID, syscall.SIGKILL) })

	direct, err := launchbroker.Send(client, launchbroker.Request{
		Argv:        []string{"sleep", "60"},
		Environment: []string{"PATH=/usr/bin:/bin"},
		Dir:         "/",
		Detach:      true,
	}, null, null, null)
	testutil.FailErr(t, "send direct detached request", err)
	started, err := launchbroker.ReadEvent(direct)
	testutil.FailErr(t, "read direct started", err)
	if started.Started <= 0 {
		t.Fatalf("direct detached started = %+v", started)
	}
	t.Cleanup(func() { _ = syscall.Kill(-started.Started, syscall.SIGKILL) })

	_ = channel.Close()
	_ = direct.Close()
	testutil.FailErr(t, "close client", client.Close())

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("broker agent stayed alive waiting on detached process")
	}
	if _, err := os.Stat("/proc/" + strconv.Itoa(bgPID)); err != nil {
		t.Fatalf("detached background process %d died when channel/broker closed: %v", bgPID, err)
	}
	if _, err := os.Stat("/proc/" + strconv.Itoa(started.Started)); err != nil {
		t.Fatalf("detached direct process %d died when channel/broker closed: %v", started.Started, err)
	}
}

func TestBrokerEndsAbandonedNonDetachedCallAndSupervisesEmptyTree(t *testing.T) {
	_, client, done := startTestBrokerAgent(t)
	null, err := os.Open("/dev/null")
	testutil.FailErr(t, "open /dev/null", err)
	defer func() { _ = null.Close() }()

	missing, err := launchbroker.Send(client, launchbroker.Request{
		Argv:        []string{"definitely-missing-binary-for-test"},
		Environment: []string{"PATH=/usr/bin:/bin"},
		Dir:         "/",
	}, null, null, null)
	testutil.FailErr(t, "send missing binary", err)
	missingEvt, err := launchbroker.ReadEvent(missing)
	testutil.FailErr(t, "read missing event", err)
	_ = missing.Close()
	if missingEvt.Error == "" {
		t.Fatalf("expected error event for missing binary, got %+v", missingEvt)
	}

	sleeper, err := launchbroker.Send(client, launchbroker.Request{
		Argv:        []string{"sleep", "60"},
		Environment: []string{"PATH=/usr/bin:/bin"},
		Dir:         "/",
	}, null, null, null)
	testutil.FailErr(t, "send sleeper", err)
	started, err := launchbroker.ReadEvent(sleeper)
	testutil.FailErr(t, "read sleeper started", err)
	_ = sleeper.Close()
	for i := 0; i < 200; i++ {
		if _, err := os.Stat("/proc/" + strconv.Itoa(started.Started)); err != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = client.Close()
	<-done

	lifetime, err := os.Open("/dev/null")
	testutil.FailErr(t, "open lifetime", err)
	if code := superviseLaunch(lifetime); code != 0 {
		t.Fatalf("superviseLaunch = %d, want 0", code)
	}

	cmd := exec.Command("sh", "-c", "exit 3")
	testutil.FailErr(t, "start exit 3", cmd.Start())
	lifetime2, err := os.Open("/dev/null")
	testutil.FailErr(t, "open lifetime2", err)
	if code := superviseLaunch(lifetime2); code != 3 {
		t.Fatalf("superviseLaunch = %d, want 3", code)
	}

	sigCmd := exec.Command("sh", "-c", "kill -9 $$")
	testutil.FailErr(t, "start kill -9", sigCmd.Start())
	lifetime3, err := os.Open("/dev/null")
	testutil.FailErr(t, "open lifetime3", err)
	if code := superviseLaunch(lifetime3); code != 128+int(unix.SIGKILL) {
		t.Fatalf("superviseLaunch = %d, want %d", code, 128+int(unix.SIGKILL))
	}

	// Malformed/oversized broker messages and start-while-closing.
	agent2, client2, done2 := startTestBrokerAgent(t)
	_, _ = unix.Write(int(client2.Fd()), []byte("{invalid"))
	_, _ = unix.Write(int(client2.Fd()), []byte(strings.Repeat("x", launchbroker.MaxRequest+2)))
	agent2.mu.Lock()
	agent2.closing = true
	agent2.mu.Unlock()
	closingChan, err := launchbroker.Send(client2, launchbroker.Request{
		Argv:        []string{"true"},
		Environment: []string{"PATH=/usr/bin:/bin"},
		Dir:         "/",
	}, null, null, null)
	testutil.FailErr(t, "send while closing", err)
	closingEvt, err := launchbroker.ReadEvent(closingChan)
	testutil.FailErr(t, "read closing event", err)
	_ = closingChan.Close()
	if !strings.Contains(closingEvt.Error, "broker is closing") {
		t.Fatalf("expected broker is closing error, got %+v", closingEvt)
	}
	_ = client2.Close()
	<-done2
}

func TestBrokerAgentIdentityAndBootstrapHelpers(t *testing.T) {
	t.Cleanup(func() {
		_ = unix.Prctl(unix.PR_SET_DUMPABLE, 1, 0, 0, 0)
	})
	if os.Getuid() != 0 {
		if _, err := openSelf(); err == nil {
			t.Fatal("expected openSelf to reject non-root test binary before trust anchor override")
		}
	}
	withTestTrustAnchor(t, t.TempDir())
	if self, err := openSelf(); err == nil {
		_ = self.Close()
	}
	testutil.FailErr(t, "initialUserNamespace", initialUserNamespace())
	// The unprivileged test process is its own original caller.
	groups, err := unix.Getgroups()
	testutil.FailErr(t, "Getgroups", err)
	spec := entryFixture()
	// The broker pair's peer is this process, not the supervisor the frame names.
	spec.SupervisorPID = os.Getppid()
	spec.UID = uint32(os.Getuid())
	spec.GID = uint32(os.Getgid())
	spec.Groups = make([]uint32, len(groups))
	for i, group := range groups {
		spec.Groups[i] = uint32(group)
	}
	testutil.FailErr(t, "sealBrokerAgentIdentity", sealBrokerAgentIdentity(spec))
	frame, err := sealEntrySpec(spec)
	testutil.FailErr(t, "sealEntrySpec", err)
	server, client, err := brokerPair()
	testutil.FailErr(t, "brokerPair", err)
	defer func() { _ = client.Close() }()
	if err := runBrokerAgent(frame, server); err == nil {
		t.Fatal("expected runBrokerAgent to reject a peer other than the named supervisor")
	}
	badFrame, err := os.Open("/dev/null")
	testutil.FailErr(t, "open /dev/null", err)
	server2, client2, err := brokerPair()
	testutil.FailErr(t, "brokerPair2", err)
	defer func() { _ = client2.Close() }()
	if err := runBrokerAgent(badFrame, server2); err == nil {
		t.Fatal("expected runBrokerAgent to reject invalid frame")
	}
	if !DropPrivilegesForError() {
		t.Fatal("expected DropPrivilegesForError to report unprivileged caller")
	}
	if err := RunStage(); err == nil {
		t.Fatal("expected RunStage to reject non-root caller")
	}
	if err := RunBrokerAgent(); err == nil {
		t.Fatal("expected RunBrokerAgent to reject unsealed fd")
	}
}

func TestClosingBrokerAgentWaitsOnlyForAttachedCalls(t *testing.T) {
	agent := &brokerAgent{closing: true, calls: map[int]*brokerCall{
		1: {pid: 10, detach: true},
		2: {pid: 11},
	}}
	if agent.shouldExit(false) {
		t.Fatal("closing agent exited while an attached call is running")
	}
	agent.calls[2].detach = true
	if !agent.shouldExit(false) {
		t.Fatal("closing agent waited on detached calls")
	}
}

func TestStandardStreamReplacementsReportOneLineEach(t *testing.T) {
	saved, err := unix.Dup(2)
	testutil.FailErr(t, "save stderr", err)
	read, write, err := os.Pipe()
	testutil.FailErr(t, "pipe", err)
	testutil.FailErr(t, "redirect stderr", unix.Dup3(int(write.Fd()), 2, 0))
	reportStandardStreamReplacements([]standardStreamReplacement{
		{Event: "protected_standard_stream_replaced", Descriptor: 0, FileType: unix.S_IFREG},
		{Event: "protected_standard_stream_replaced", Descriptor: 2, FileType: unix.S_IFDIR},
	})
	testutil.FailErr(t, "restore stderr", unix.Dup3(saved, 2, 0))
	_ = unix.Close(saved)
	_ = write.Close()
	raw, err := io.ReadAll(read)
	_ = read.Close()
	testutil.FailErr(t, "read report", err)
	want := fmt.Sprintf("{\"event\":\"protected_standard_stream_replaced\",\"fd\":0,\"file_type\":%d}\n{\"event\":\"protected_standard_stream_replaced\",\"fd\":2,\"file_type\":%d}\n", unix.S_IFREG, unix.S_IFDIR)
	if string(raw) != want {
		t.Fatalf("report = %q, want %q", raw, want)
	}
}

func TestBindGenerationRefusesAWritableGeneration(t *testing.T) {
	base := t.TempDir()
	withTestTrustAnchor(t, base)
	writable := filepath.Join(base, "generation")
	testutil.FailErr(t, "mkdir generation", os.Mkdir(writable, 0o755))
	testutil.FailErr(t, "loosen generation", os.Chmod(writable, 0o777))
	generation, err := os.Open(writable)
	testutil.FailErr(t, "open generation", err)
	defer func() { _ = generation.Close() }()
	if mounted, err := bindGeneration(GenerationPolicy{}, generation, nil); err == nil {
		_ = mounted.Close()
		t.Fatal("bound a world-writable generation")
	}
}

func TestEntryFrameAndTrustedPathRefuseMismatchedObjects(t *testing.T) {
	if frame, err := sealEntrySpec(EntrySpec{Version: 2}); err == nil {
		_ = frame.Close()
		t.Fatal("sealed an entry specification its reader refuses")
	}
	base := t.TempDir()
	withTestTrustAnchor(t, base)
	directory := filepath.Join(base, "directory")
	testutil.FailErr(t, "mkdir", os.Mkdir(directory, 0o755))
	if file, err := openRootControlled(directory, false); err == nil {
		_ = file.Close()
		t.Fatal("opened a directory where a regular file is required")
	}
}
