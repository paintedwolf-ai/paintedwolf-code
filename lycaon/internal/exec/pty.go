package exec

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/argv"
)

// Default pseudo-terminal geometry and environment.
const (
	DefaultPTYCols uint16 = 80
	DefaultPTYRows uint16 = 24
	DefaultTERM           = "xterm-256color"
)

// ErrPTYUnsupported marks a missing platform backend.
var ErrPTYUnsupported = errors.New("pty unsupported on this platform")

// WinSize is a pty window size in character cells.
type WinSize struct {
	Cols uint16
	Rows uint16
}

// PTYOpts configures a pseudo-terminal subprocess.
type PTYOpts struct {
	Dir            string
	Timeout        time.Duration
	MaxOutputBytes int
	Env            []string
	InlineEnv      map[string]string
	Launch         LaunchPlan
	NoTimeout      bool
	WinSize        WinSize
	// PathExtra mirrors ExecOpts.PathExtra for interactive sessions.
	PathExtra []string
}

// PTY is a live pseudo-terminal attached to a child process.
type PTY interface {
	io.ReadWriteCloser
	Resize(size WinSize) error
	Wait() error
	Kill()
	Pid() int
	Cmd() *exec.Cmd
}

// ptySession is the shared concrete PTY handle.
type ptySession struct {
	main    *os.File
	cmd     *exec.Cmd
	guard   runGuard
	cleanup func()

	waitOnce sync.Once
	waitErr  error
	mu       sync.Mutex
	closed   bool
}

func (p *ptySession) Read(b []byte) (int, error)  { return p.main.Read(b) }
func (p *ptySession) Write(b []byte) (int, error) { return p.main.Write(b) }

func (p *ptySession) Pid() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return -1
	}
	return p.cmd.Process.Pid
}

func (p *ptySession) Cmd() *exec.Cmd { return p.cmd }

func (p *ptySession) Wait() error {
	p.waitOnce.Do(func() {
		if p.cmd != nil {
			p.waitErr = p.cmd.Wait()
		}
	})
	return p.waitErr
}

func (p *ptySession) Kill() {
	if p.guard != nil {
		p.guard.kill(p.cmd)
		return
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (p *ptySession) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()

	p.Kill()
	_ = p.Wait()
	var err error
	if p.main != nil {
		err = p.main.Close()
	}
	if p.cleanup != nil {
		p.cleanup()
	}
	if p.guard != nil {
		p.guard.release()
	}
	return err
}

func normalizeWinSize(size WinSize) WinSize {
	if size.Cols == 0 {
		size.Cols = DefaultPTYCols
	}
	if size.Rows == 0 {
		size.Rows = DefaultPTYRows
	}
	return size
}

func (o PTYOpts) toExecOpts() ExecOpts {
	return ExecOpts{
		Launch:         o.Launch,
		Dir:            o.Dir,
		Timeout:        o.Timeout,
		MaxOutputBytes: o.MaxOutputBytes,
		Env:            o.Env,
		InlineEnv:      o.InlineEnv,
		NoTimeout:      o.NoTimeout,
		PathExtra:      o.PathExtra,
	}
}

// applyDefaultTERM stabilizes terminal behavior unless the caller sets TERM.
func applyDefaultTERM(env []string, providedEnv []string, inline map[string]string) []string {
	if _, ok := inline["TERM"]; ok {
		return env
	}
	for _, entry := range providedEnv {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "TERM") {
			return env
		}
	}
	return replaceEnvKey(env, "TERM", DefaultTERM)
}

func replaceEnvKey(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	found := false
	for _, entry := range env {
		k, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(k, key) {
			if !found {
				out = append(out, key+"="+value)
				found = true
			}
			continue
		}
		out = append(out, entry)
	}
	if !found {
		out = append(out, key+"="+value)
	}
	return out
}

func validatePTYCommand(name string) error {
	if strings.TrimSpace(name) == "" {
		return argv.ErrCommandRequired
	}
	if argv.ContainsShellMetacharacters(name) {
		return fmt.Errorf("%w: command name", argv.ErrShellMetacharacters)
	}
	return nil
}
