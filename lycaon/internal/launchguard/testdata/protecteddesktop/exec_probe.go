//go:build linux

package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/exec"
)

// ExecProbe reports what the engine's own launch paths did inside the
// protected namespace: which namespace each program ran in, and whether
// terminal interrupts and relay loss behaved.
type ExecProbe struct {
	HostOutput      string `json:"host_output"`
	HostCode        int    `json:"host_code"`
	HostError       string `json:"host_error,omitempty"`
	HostExecOutput  string `json:"host_execution_output"`
	HostExecCode    int    `json:"host_execution_code"`
	HostExecError   string `json:"host_execution_error,omitempty"`
	ConfinedOutput  string `json:"confined_output"`
	ConfinedError   string `json:"confined_error,omitempty"`
	ConfinedCode    int    `json:"confined_code"`
	ConfinedStderr  string `json:"confined_stderr,omitempty"`
	TerminalSurvive bool   `json:"terminal_survived_interrupt"`
	TerminalError   string `json:"terminal_error,omitempty"`
	RelayLossEnded  bool   `json:"relay_loss_ended_session"`
	RelayLossError  string `json:"relay_loss_error,omitempty"`
}

const namespaceScript = "readlink /proc/self/ns/user; grep NoNewPrivs /proc/self/status; exit 7"

func execProbe(scratch string) ExecProbe {
	var probe ExecProbe
	// The desktop hands the engine the broker the same way.
	_ = os.Setenv(exec.LaunchBrokerFDEnv, "6")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, _, code, err := exec.RunSeparate(ctx, "sh", []string{"-c", namespaceScript}, exec.ExecOpts{Launch: exec.HostLaunch("protected launch probe")})
	probe.HostOutput, probe.HostCode = string(out), code
	if err != nil {
		probe.HostError = err.Error()
	}
	out, _, code, err = exec.RunSeparate(ctx, "sh", []string{"-c", namespaceScript}, exec.ExecOpts{
		Launch: exec.AgentLaunch(exec.LaunchAgentCommand, "protected launch probe", &confine.Confinement{HostExecution: true}),
	})
	probe.HostExecOutput, probe.HostExecCode = string(out), code
	if err != nil {
		probe.HostExecError = err.Error()
	}
	confinement := &confine.Confinement{Roots: []string{scratch}, Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride}
	out, stderr, code, err := exec.RunSeparate(ctx, "sh", []string{"-c", namespaceScript}, exec.ExecOpts{
		Launch: exec.AgentLaunch(exec.LaunchAgentCommand, "protected launch probe", confinement),
	})
	probe.ConfinedOutput, probe.ConfinedCode, probe.ConfinedStderr = string(out), code, string(stderr)
	if err != nil {
		probe.ConfinedError = err.Error()
	}
	probe.TerminalSurvive, probe.TerminalError = terminalProbe(ctx)
	probe.RelayLossEnded, probe.RelayLossError = relayLossProbe(ctx)
	return probe
}

// terminal accumulates what a PTY session prints.
type terminal struct {
	mu  sync.Mutex
	buf strings.Builder
}

func watch(session exec.PTY) *terminal {
	t := &terminal{}
	go func() {
		chunk := make([]byte, 4096)
		for {
			n, err := session.Read(chunk)
			t.mu.Lock()
			t.buf.Write(chunk[:n])
			t.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return t
}

func (t *terminal) await(marker string) bool {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		t.mu.Lock()
		seen := strings.Contains(t.buf.String(), marker)
		t.mu.Unlock()
		if seen {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (t *terminal) text() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

// terminalProbe runs an interactive interpreter on a brokered terminal and
// requires it to survive Ctrl-C.
func terminalProbe(ctx context.Context) (bool, string) {
	session, err := exec.StartPTY(ctx, "python3", []string{"-i", "-q", "-u"}, exec.PTYOpts{Launch: exec.HostLaunch("protected terminal probe"), Timeout: 30 * time.Second})
	if err != nil {
		return false, err.Error()
	}
	defer session.Close()
	output := watch(session)
	send := func(text string) { _, _ = session.Write([]byte(text)) }
	send("print('ready' + '-for-input')\n")
	if !output.await("ready-for-input") {
		return false, "never ready: " + output.text()
	}
	send("\x03")
	if !output.await("KeyboardInterrupt") {
		return false, "no interrupt: " + output.text()
	}
	send("print('interrupt' + '-survived')\n")
	if !output.await("interrupt-survived") {
		return false, "did not survive: " + output.text()
	}
	send("raise SystemExit(0)\n")
	if err := session.Wait(); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// relayLossProbe kills the relay standing in for a brokered supervisor and
// requires the program it started to end.
func relayLossProbe(ctx context.Context) (bool, string) {
	session, err := exec.StartPTY(ctx, "sh", []string{"-c", "echo started-$$; exec sleep 60"}, exec.PTYOpts{Launch: exec.HostLaunch("protected relay probe"), Timeout: 60 * time.Second})
	if err != nil {
		return false, err.Error()
	}
	defer session.Close()
	output := watch(session)
	if !output.await("started-") {
		return false, "never started: " + output.text()
	}
	text := output.text()
	field := text[strings.Index(text, "started-")+len("started-"):]
	pid, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(field, "\n", 2)[0]))
	if err != nil || pid <= 0 {
		return false, "no pid in " + text
	}
	if err := unix.Kill(session.Pid(), unix.SIGKILL); err != nil {
		return false, err.Error()
	}
	_ = session.Wait()
	for i := 0; i < 250; i++ {
		if _, err := os.Stat("/proc/" + strconv.Itoa(pid)); err != nil {
			return true, ""
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false, "the brokered program outlived its relay"
}
