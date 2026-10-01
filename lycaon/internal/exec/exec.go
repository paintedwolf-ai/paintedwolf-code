package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/argv"
)

// ProcessPriority selects OS scheduling priority for a child process.
type ProcessPriority string

const (
	// ProcessPriorityNormal is the OS default priority class.
	ProcessPriorityNormal ProcessPriority = "normal"
	// ProcessPriorityBelowNormal yields the child under CPU contention.
	ProcessPriorityBelowNormal ProcessPriority = "below_normal"
)

// ExecOpts configures a bounded subprocess invocation.
type ExecOpts struct {
	// Launch is mandatory authority and attribution for the subprocess.
	Launch         LaunchPlan
	Dir            string
	Timeout        time.Duration
	MaxOutputBytes int
	// KeepOutputTail retains trailing bytes when output is capped.
	KeepOutputTail bool
	Env            []string
	// InlineEnv is merged into the sanitized inherited environment for every stage.
	InlineEnv map[string]string
	// AppendEnv carries explicit values after inherited-environment filtering.
	AppendEnv []string
	// PathExtra appends host-derived resource directories to PATH.
	PathExtra []string
	Stdin     *StdinSpec
	Redirect  *RedirectSpec
	// NoTimeout skips the run-to-completion deadline (background pipelines).
	NoTimeout bool
	// SeparateStderr preserves stdout for structured parsing.
	SeparateStderr bool
	// Stdout streams complete output to a host-owned consumer instead of capture.
	// Stderr remains bounded diagnostics. The consumer must not block indefinitely.
	Stdout io.Writer
	// ProcessPriority applies ScanProcessPolicy / engine below-normal posture when set.
	ProcessPriority ProcessPriority
}

// DefaultVerifyTimeout is the default verify subprocess timeout.
const DefaultVerifyTimeout = 10 * time.Minute

// DefaultCommandWait bounds an inline foreground wait.
const DefaultCommandWait = 30 * time.Second

// MaxCommandWait caps an inline wait override.
const MaxCommandWait = 30 * time.Second

// MaxCommandTimeout bounds an agent-selected process deadline.
const MaxCommandTimeout = 2 * time.Hour

// DefaultGitTimeout is the default git subprocess timeout.
const DefaultGitTimeout = 2 * time.Minute

// DefaultMaxOutputBytes caps combined stdout+stderr capture.
const DefaultMaxOutputBytes = 1 << 20

// DefaultMaxScanOutputBytes caps machine-readable scanner output.
const DefaultMaxScanOutputBytes = 64 << 20

// ErrOutputTruncated is returned when subprocess output exceeds MaxOutputBytes.
var ErrOutputTruncated = errors.New("subprocess output truncated")

// ErrTimeout is returned when a subprocess was killed for exceeding its deadline.
var ErrTimeout = errors.New("subprocess timed out")

// LocalGitEnv filters inherited state before appending explicit values.
func LocalGitEnv(extra ...string) []string {
	env := InheritedEnviron()
	for _, e := range extra {
		if strings.TrimSpace(e) != "" {
			env = append(env, e)
		}
	}
	return env
}

// Run executes name and arguments without a shell.
func Run(ctx context.Context, name string, args []string, opts ExecOpts) ([]byte, int, error) {
	if strings.TrimSpace(name) == "" {
		return nil, -1, argv.ErrCommandRequired
	}
	if argv.ContainsShellMetacharacters(name) {
		return nil, -1, fmt.Errorf("%w: command name", argv.ErrShellMetacharacters)
	}

	res, err := RunPipeline(ctx, []Stage{{Name: name, Args: args}}, opts)
	if res == nil {
		return nil, -1, err
	}
	exitCode := res.ExitCode
	if res.TimedOut {
		return res.Output, -1, err
	}
	if err != nil && !errors.Is(err, ErrOutputTruncated) {
		if exitCode < 0 {
			return res.Output, -1, err
		}
	}
	if errors.Is(err, ErrOutputTruncated) {
		return res.Output, exitCode, err
	}
	return res.Output, exitCode, nil
}

// RunSeparate executes name+args with stdout and stderr captured independently.
// Use it when stdout is parsed as structured data and stderr is only diagnostics.
func RunSeparate(ctx context.Context, name string, args []string, opts ExecOpts) (stdout, stderr []byte, exitCode int, err error) {
	opts.SeparateStderr = true
	res, runErr := RunPipeline(ctx, []Stage{{Name: name, Args: args}}, opts)
	if res == nil {
		return nil, nil, -1, runErr
	}
	exitCode = res.ExitCode
	if res.TimedOut {
		return res.Output, res.Stderr, -1, runErr
	}
	if runErr != nil && !errors.Is(runErr, ErrOutputTruncated) && exitCode < 0 {
		return res.Output, res.Stderr, -1, runErr
	}
	if errors.Is(runErr, ErrOutputTruncated) {
		return res.Output, res.Stderr, exitCode, runErr
	}
	return res.Output, res.Stderr, exitCode, nil
}

type limitedWriter struct {
	w         io.Writer
	limit     int
	n         int
	truncated bool
}

func (lw *limitedWriter) Write(p []byte) (int, error) {
	origLen := len(p)
	if lw.n >= lw.limit {
		lw.truncated = true
		return origLen, nil
	}
	remain := lw.limit - lw.n
	if origLen > remain {
		lw.truncated = true
		p = p[:remain]
	}
	if _, err := lw.w.Write(p); err != nil {
		return 0, err
	}
	lw.n += len(p)
	return origLen, nil
}

// tailLimitedWriter keeps the last limit bytes of a stream (sliding window).
type tailLimitedWriter struct {
	buf       []byte
	limit     int
	truncated bool
}

func (tw *tailLimitedWriter) Write(p []byte) (int, error) {
	origLen := len(p)
	if tw.limit <= 0 {
		tw.truncated = tw.truncated || origLen > 0
		return origLen, nil
	}
	if len(p) >= tw.limit {
		tw.truncated = true
		tw.buf = append(tw.buf[:0], p[len(p)-tw.limit:]...)
		return origLen, nil
	}
	need := len(tw.buf) + len(p)
	if need <= tw.limit {
		tw.buf = append(tw.buf, p...)
		return origLen, nil
	}
	tw.truncated = true
	drop := need - tw.limit
	tw.buf = append(tw.buf[drop:], p...)
	return origLen, nil
}

func (tw *tailLimitedWriter) Bytes() []byte {
	return tw.buf
}

// cappedOutput serializes merged stdout and stderr under one byte cap.
type cappedOutput struct {
	mu   sync.Mutex
	head *limitedWriter
	tail *tailLimitedWriter
	buf  bytes.Buffer
}

func newCappedOutput(limit int, keepTail bool) *cappedOutput {
	c := &cappedOutput{}
	if keepTail {
		c.tail = &tailLimitedWriter{limit: limit}
		return c
	}
	c.head = &limitedWriter{w: &c.buf, limit: limit}
	return c
}

func (c *cappedOutput) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tail != nil {
		return c.tail.Write(p)
	}
	return c.head.Write(p)
}

func (c *cappedOutput) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tail != nil {
		return c.tail.Bytes()
	}
	return c.buf.Bytes()
}

func (c *cappedOutput) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tail != nil {
		return c.tail.truncated
	}
	return c.head != nil && c.head.truncated
}
