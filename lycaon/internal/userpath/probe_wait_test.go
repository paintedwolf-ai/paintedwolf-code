package userpath

import (
	"context"
	"errors"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/osprocess"
)

func TestProbeValidatesOutputAfterWaitDelay(t *testing.T) {
	cfg := testConfig()
	complete := framed(cfg.Probe.Marker, "/opt/toolchain/bin:/usr/bin")
	cases := []struct {
		name    string
		output  string
		err     error
		source  Source
		failure Failure
	}{
		{"complete answer", complete, osexec.ErrWaitDelay, SourceProbe, FailureNone},
		{"missing answer", "banner\n", osexec.ErrWaitDelay, SourceInherited, FailureProbeFailed},
		{"incomplete answer", cfg.Probe.Marker + "\n/opt/toolchain/bin\n", osexec.ErrWaitDelay, SourceInherited, FailureProbeFailed},
		{"oversized output", complete + strings.Repeat("x", cfg.Probe.MaxOutputBytes), osexec.ErrWaitDelay, SourceInherited, FailureProbeFailed},
		{"invalid path", framed(cfg.Probe.Marker, "relative"), osexec.ErrWaitDelay, SourceInherited, FailurePathInvalid},
		{"failed shell with answer", complete, errors.New("shell failed"), SourceInherited, FailureProbeFailed},
		{"canceled shell with answer", complete, context.DeadlineExceeded, SourceInherited, FailureProbeFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := providerWith(t, cfg, "/bin/zsh", map[string]string{
				"SHELL": "/bin/zsh", "PATH": "/usr/bin",
			}, fakeShell{stdout: tc.output, err: tc.err})
			snap := p.Resolve(context.Background())
			if snap.Source() != tc.source || snap.Failure() != tc.failure {
				t.Fatalf("source/failure = %q/%q (%s), want %q/%q", snap.Source(), snap.Failure(), snap.Reason(), tc.source, tc.failure)
			}
			if tc.source == SourceProbe && (snap.Value() != "/opt/toolchain/bin:/usr/bin" || snap.Reason() != "") {
				t.Fatalf("path/reason = %q/%q, want the shell answer without a warning", snap.Value(), snap.Reason())
			}
		})
	}
}

func TestProbeAcceptsAnswerWithBackgroundChildHoldingStdout(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "background.pid")
	cfg := testConfig()
	cfg.Probe.Timeout = 5 * time.Second
	cfg.Shells = []ShellInvocation{{
		Names: []string{"sh"},
		Args:  []string{"-c"},
		Command: `/bin/sleep 30 & echo $! > ` + strconv.Quote(pidFile) +
			`; printf "%s\n%s\n%s\n" "{marker}" "/opt/toolchain/bin:/usr/bin" "{marker}"`,
	}}
	p := providerWith(t, cfg, "/bin/sh", map[string]string{
		"SHELL": "/bin/sh", "PATH": "/usr/bin",
	}, fakeShell{})
	var runErr error
	p.run = func(cmd *osexec.Cmd) error {
		runErr = defaultRun(cmd)
		return runErr
	}
	snap := p.Resolve(context.Background())
	if !errors.Is(runErr, osexec.ErrWaitDelay) {
		t.Fatalf("shell result = %v, want the background child to exhaust WaitDelay", runErr)
	}
	if snap.Source() != SourceProbe || snap.Value() != "/opt/toolchain/bin:/usr/bin" {
		t.Fatalf("source/path = %q/%q (%s), want the complete shell answer", snap.Source(), snap.Value(), snap.Reason())
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read background pid: %v", err)
	}
	background, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("parse background pid %q: %v", raw, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for osprocess.Alive(background) {
		if time.Now().After(deadline) {
			_ = (&os.Process{Pid: background}).Kill()
			t.Fatalf("profile helper %d outlived the probe that started it", background)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
