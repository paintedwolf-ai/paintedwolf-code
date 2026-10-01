package userpath

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	execpkg "github.com/lycaon/lycaon/internal/exec"
)

// errProbeUnavailable marks an unavailable probe.
var errProbeUnavailable = errors.New("no usable login shell")

// errEmptyProbeResult marks a PATH without absolute entries.
var errEmptyProbeResult = errors.New("login shell reported no absolute PATH entries")

const probeWaitDelay = 500 * time.Millisecond

// probeShell reads a bounded, framed PATH from the login shell.
func (p *Provider) probeShell(ctx context.Context) (string, error) {
	shell, err := p.loginShell(ctx)
	if err != nil {
		return "", err
	}
	invocation, known := p.cfg.invocationFor(filepath.Base(shell))
	if !known {
		return "", fmt.Errorf("%w: %s is not a shell this build knows how to invoke", errProbeUnavailable, filepath.Base(shell))
	}

	probe := p.cfg.Probe
	command := strings.ReplaceAll(invocation.Command, MarkerPlaceholder, probe.Marker)
	args := append(append([]string(nil), invocation.Args...), command)

	ctx, cancel := context.WithTimeout(ctx, probe.Timeout)
	defer cancel()

	cmd, cleanup, err := execpkg.PrepareCommand(ctx, shell, args, execpkg.ExecOpts{
		Launch: execpkg.HostLaunch("user_path_probe"), Env: p.probeEnv(shell),
	})
	if err != nil {
		return "", err
	}
	defer cleanup()
	// Bound pipes held open by background children.
	cmd.WaitDelay = probeWaitDelay
	cmd.Stdin = nil

	stdout := boundedBuffer{limit: probe.MaxOutputBytes}
	cmd.Stdout = &stdout
	cmd.Stderr = nil

	// ErrWaitDelay preserves the successful shell's captured output.
	if err := p.run(cmd); err != nil && !errors.Is(err, osexec.ErrWaitDelay) {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("login shell did not answer within %s", probe.Timeout)
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("login shell probe canceled: %w", ctx.Err())
		}
		return "", fmt.Errorf("login shell exited without answering: %w", err)
	}
	if stdout.overflow {
		return "", fmt.Errorf("login shell wrote more than %d bytes", probe.MaxOutputBytes)
	}
	return framedValue(stdout.String(), probe.Marker)
}

// boundedBuffer drains excess bytes without growing the capture.
type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.overflow = b.overflow || written > 0
		return written, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(value)
	return written, nil
}

// loginShell prefers the account record because GUI processes may not inherit SHELL.
func (p *Provider) loginShell(ctx context.Context) (string, error) {
	accountShell, accountErr := p.accountShell(ctx)
	if shell, err := p.validateShell(accountShell); err == nil {
		return shell, nil
	} else if strings.TrimSpace(accountShell) != "" {
		accountErr = err
	}

	environmentShell, _ := p.lookupEnv("SHELL")
	if shell, err := p.validateShell(environmentShell); err == nil {
		return shell, nil
	} else if accountErr != nil {
		return "", fmt.Errorf("%w: account shell: %w; environment shell: %w", errProbeUnavailable, accountErr, err)
	} else {
		return "", err
	}
}

func (p *Provider) validateShell(raw string) (string, error) {
	shell := strings.TrimSpace(raw)
	switch {
	case shell == "":
		return "", fmt.Errorf("%w: shell is unset", errProbeUnavailable)
	case !filepath.IsAbs(shell):
		return "", fmt.Errorf("%w: shell %q is not an absolute path", errProbeUnavailable, shell)
	case strings.ContainsAny(shell, "\x00\n\r"):
		return "", fmt.Errorf("%w: shell contains a control character", errProbeUnavailable)
	}
	info, err := p.stat(shell)
	if err != nil {
		return "", fmt.Errorf("%w: shell %q is not readable: %w", errProbeUnavailable, shell, err)
	}
	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%w: shell %q is not executable", errProbeUnavailable, shell)
	}
	return shell, nil
}

// probeEnv passes only variables required by the configured probe.
func (p *Provider) probeEnv(shell string) []string {
	allowed := make(map[string]struct{}, len(p.cfg.Probe.EnvAllowlist))
	for _, key := range p.cfg.Probe.EnvAllowlist {
		allowed[key] = struct{}{}
	}
	out := make([]string, 0, len(allowed))
	for _, entry := range p.environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, permitted := allowed[key]; permitted {
			if key == "SHELL" {
				continue
			}
			out = append(out, entry)
		}
	}
	if _, permitted := allowed["SHELL"]; permitted {
		out = append(out, "SHELL="+shell)
	}
	return out
}

func classifyProbeFailure(err error) Failure {
	switch {
	case errors.Is(err, errProbeUnavailable):
		return FailureShellUnavailable
	case errors.Is(err, errEmptyProbeResult):
		return FailurePathInvalid
	default:
		return FailureProbeFailed
	}
}

// framedValue requires one line between two complete markers.
func framedValue(output, marker string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != marker {
			continue
		}
		if start < 0 {
			start = i
			continue
		}
		if i == start+2 {
			return lines[start+1], nil
		}
		return "", fmt.Errorf("login shell framed %d lines where one was expected", i-start-1)
	}
	if start < 0 {
		return "", errors.New("login shell output carried no marker; its profile may have failed")
	}
	return "", errors.New("login shell output was cut off before the closing marker")
}

func defaultStat(path string) (fs.FileInfo, error) { return os.Stat(path) }

// defaultRun kills the shell and any profile helpers as one group on timeout,
// after answering, or with the engine; interactive shells ignore SIGTERM.
func defaultRun(cmd *osexec.Cmd) error { return execpkg.RunInOwnGroup(cmd) }
