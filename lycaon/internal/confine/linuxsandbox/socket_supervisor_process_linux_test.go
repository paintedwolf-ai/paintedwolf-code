//go:build linux

package linuxsandbox_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

type socketSupervisorProcess struct {
	ready  *os.File
	resume *os.File
	cancel context.CancelFunc
	done   chan error
	output bytes.Buffer
	joined bool
}

// Relaunch owns process-global standard streams; the real owner runs in an
// owned subprocess while the test synchronizes its connect through two pipes.
func startSocketSupervisorProcess(t *testing.T, command *exec.Cmd) *socketSupervisorProcess {
	t.Helper()
	readyRead, readyWrite, err := os.Pipe()
	testutil.FailErr(t, "supervisor ready pipe", err)
	resumeRead, resumeWrite, err := os.Pipe()
	if err != nil {
		_ = readyRead.Close()
		_ = readyWrite.Close()
		testutil.FailErr(t, "supervisor resume pipe", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	process := &socketSupervisorProcess{ready: readyRead, resume: resumeWrite, cancel: cancel, done: make(chan error, 1), joined: true}
	t.Cleanup(process.close)
	self, err := os.Executable()
	testutil.FailErr(t, "supervisor test executable", err)
	args, err := json.Marshal(command.Args[1:])
	testutil.FailErr(t, "supervisor helper arguments", err)
	helper := exec.CommandContext(ctx, self, "-test.run=^TestLinuxSocketSupervisorProcess$")
	helper.Env = append(os.Environ(), "PW_SOCKET_SUPERVISOR_PROCESS=1", "PW_SOCKET_SUPERVISOR_ARGS="+string(args))
	helper.ExtraFiles = []*os.File{command.ExtraFiles[0], command.ExtraFiles[len(command.ExtraFiles)-1], readyWrite, resumeRead}
	helper.Stdout, helper.Stderr = &process.output, &process.output
	err = helper.Start()
	_ = readyWrite.Close()
	_ = resumeRead.Close()
	if err != nil {
		cancel()
		process.joined = true
		testutil.FailErr(t, "start supervisor owner process", err)
	}
	process.joined = false
	go func() { process.done <- helper.Wait() }()
	return process
}

func (p *socketSupervisorProcess) awaitPause(t *testing.T) {
	t.Helper()
	var signal [1]byte
	if _, err := io.ReadFull(p.ready, signal[:]); err != nil {
		completion := <-p.done
		p.joined = true
		t.Fatalf("supervisor did not pause: %v; completion=%v; output=%s", err, completion, p.output.String())
	}
	if signal[0] != 1 {
		t.Fatalf("invalid supervisor pause marker: %v", signal)
	}
}
func (p *socketSupervisorProcess) resumeAndWaitResult(t *testing.T, success bool) {
	t.Helper()
	_, err := p.resume.Write([]byte{1})
	testutil.FailErr(t, "resume actual supervisor", err)
	err = <-p.done
	p.joined = true
	if (err == nil) != success {
		t.Fatalf("actual supervisor completion: %v; output=%s", err, p.output.String())
	}
}
func (p *socketSupervisorProcess) close() {
	if !p.joined {
		p.cancel()
		<-p.done
		p.joined = true
	}
	p.cancel()
	_ = p.ready.Close()
	_ = p.resume.Close()
}

func TestLinuxSocketSupervisorProcess(t *testing.T) {
	if os.Getenv("PW_SOCKET_SUPERVISOR_PROCESS") != "1" {
		return
	}
	profileFile := os.NewFile(3, "actual command profile")
	profile, err := io.ReadAll(profileFile)
	_ = profileFile.Close()
	testutil.FailErr(t, "read actual command profile", err)
	source := os.NewFile(4, "actual command socket reference")
	defer source.Close()
	ready, resume := os.NewFile(5, "supervisor ready"), os.NewFile(6, "supervisor resume")
	defer ready.Close()
	defer resume.Close()
	plan, err := linuxsandbox.DecodePlan(profile)
	testutil.FailErr(t, "decode supervisor plan", err)
	inherited, err := unix.FcntlInt(source.Fd(), unix.F_DUPFD_CLOEXEC, linuxsandbox.SocketReferenceFD)
	testutil.FailErr(t, "retain inherited destination", err)
	defer unix.Close(inherited)
	plan.Network.SocketGrants[0].FD = inherited
	network := linuxsandbox.WithSocketConnectPauseForTest(plan.Network, func() {
		if _, err := ready.Write([]byte{1}); err != nil {
			return
		}
		var signal [1]byte
		_, _ = io.ReadFull(resume, signal[:])
	})
	var args []string
	testutil.FailErr(t, "decode supervisor arguments", json.Unmarshal([]byte(os.Getenv("PW_SOCKET_SUPERVISOR_ARGS")), &args))
	self, err := os.Executable()
	testutil.FailErr(t, "supervisor executable", err)
	code, signal, err := linuxsandbox.Relaunch(self, args, profile, network, nil, nil, nil)
	testutil.FailErr(t, "actual Relaunch owner", err)
	if code != 0 || signal != 0 {
		t.Fatalf("target exit %d, signal %v", code, signal)
	}
}
