//go:build unix

package userpath

import (
	"context"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

const terminalProbeHelperEnv = "LYCAON_USERPATH_TERMINAL_HELPER"

// An engine started from a shell holds that terminal, and zsh -i reaches for
// it on startup.
func TestProbeAnswersWhenTheEngineHoldsATerminal(t *testing.T) {
	if _, err := os.Stat("/bin/zsh"); err != nil {
		t.Skip("needs /bin/zsh")
	}
	result := filepath.Join(t.TempDir(), "result")
	helper := osexec.Command(os.Args[0], "-test.run=^TestProbeTerminalHelperProcess$")
	helper.Env = append(os.Environ(), terminalProbeHelperEnv+"="+result)
	terminal, err := pty.StartWithSize(helper, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start helper under a terminal: %v", err)
	}
	defer terminal.Close()
	go func() { _, _ = io.Copy(io.Discard, terminal) }()

	done := make(chan error, 1)
	go func() { done <- helper.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("helper: %v", err)
		}
	case <-time.After(20 * time.Second):
		_ = helper.Process.Kill()
		t.Fatal("helper never finished")
	}
	raw, err := os.ReadFile(result)
	if err != nil {
		t.Fatalf("read helper result: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != string(SourceProbe) {
		t.Fatalf("probe under a terminal resolved %s, want %s", got, SourceProbe)
	}
}

// TestProbeTerminalHelperProcess is the engine holding a terminal, not a test.
// It runs only when the parent case starts it under a pty.
func TestProbeTerminalHelperProcess(t *testing.T) {
	result := os.Getenv(terminalProbeHelperEnv)
	if result == "" {
		t.Skip("helper process for TestProbeAnswersWhenTheEngineHoldsATerminal")
	}
	cfg := testConfig()
	cfg.Probe.Timeout = 3 * time.Second
	cfg.Probe.EnvAllowlist = append(cfg.Probe.EnvAllowlist, "SHELL")
	cfg.Shells = []ShellInvocation{{
		Names:   []string{"zsh"},
		Args:    []string{"-lic"},
		Command: `printf "%s\n%s\n%s\n" "{marker}" "$PATH" "{marker}"`,
	}}
	p := providerWith(t, cfg, "/bin/zsh", map[string]string{
		"SHELL": "/bin/zsh", "PATH": "/usr/bin:/bin", "HOME": t.TempDir(),
	}, fakeShell{})
	p.run = defaultRun

	snap := p.Resolve(context.Background())
	outcome := string(snap.Source())
	if snap.Source() != SourceProbe {
		outcome += ": " + snap.Reason()
	}
	if err := os.WriteFile(result, []byte(outcome), 0o600); err != nil {
		t.Fatalf("write result: %v", err)
	}
}
