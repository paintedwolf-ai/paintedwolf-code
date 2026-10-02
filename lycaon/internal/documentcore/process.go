package documentcore

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/observability"
)

var log = observability.LazyComponent("documentcore")

const (
	// protocolVersion matches PROTOCOL in native/src/main.rs.
	protocolVersion = 1
	startTimeout    = 10 * time.Second
	stopGrace       = 2 * time.Second
	stderrTailBytes = 4 << 10
)

// ErrExited reports that the core process ended; its documents are gone.
var ErrExited = errors.New("document core exited")

// ErrTimeout reports a request the core did not answer in time.
var ErrTimeout = errors.New("document core did not answer in time")

// ExitError says how the core process ended.
type ExitError struct {
	// Status is the operating system's account, such as "signal: abort trap".
	Status string
	// Diagnostics is the tail of what the process wrote to stderr.
	Diagnostics string
}

func (e *ExitError) Error() string {
	if e.Diagnostics == "" {
		return fmt.Sprintf("%s (%s)", ErrExited, e.Status)
	}
	return fmt.Sprintf("%s (%s): %s", ErrExited, e.Status, e.Diagnostics)
}

func (e *ExitError) Unwrap() error { return ErrExited }

// process is one running core. Requests are strictly sequential: the core
// answers each frame before reading the next.
type process struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	frames  chan []byte
	done    chan struct{}
	exitErr error
	stderr  *tail
	// confined reports whether the platform sandbox applies to the process.
	confined bool

	stopOnce sync.Once
	stopping chan struct{}
}

func start(ctx context.Context, binary string) (*process, error) {
	scratch, err := os.MkdirTemp("", "pw-document-core-")
	if err != nil {
		return nil, fmt.Errorf("document core scratch: %w", err)
	}
	box, applied := confine.DefaultConfinement(confine.Request{Roots: []string{scratch}, Egress: confine.EgressDeny})
	if err := confine.RequireApplied(applied); err != nil {
		_ = os.RemoveAll(scratch)
		return nil, fmt.Errorf("document core confinement: %w", err)
	}
	args := []string{"serve", "--memory-limit-bytes", strconv.Itoa(memoryLimitBytes)}
	cmd, cleanup, err := execpkg.PrepareCommand(context.WithoutCancel(ctx), binary, args, execpkg.ExecOpts{
		Launch:    execpkg.DocumentCoreLaunch(box).WithReducedEnvironment(),
		Dir:       scratch,
		NoTimeout: true,
	})
	if err != nil {
		_ = os.RemoveAll(scratch)
		return nil, fmt.Errorf("document core prepare: %w", err)
	}
	release := func() {
		cleanup()
		_ = os.RemoveAll(scratch)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		release()
		return nil, fmt.Errorf("document core stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		release()
		return nil, fmt.Errorf("document core stdout: %w", err)
	}
	p := &process{
		cmd: cmd, stdin: stdin, stderr: &tail{limit: stderrTailBytes}, confined: box != nil,
		frames: make(chan []byte, 1), done: make(chan struct{}), stopping: make(chan struct{}),
	}
	cmd.Stderr = p.stderr
	if err := cmd.Start(); err != nil {
		release()
		return nil, fmt.Errorf("document core start: %w", err)
	}
	untrack := execpkg.TrackProcess(cmd.Process.Pid)
	go p.read(stdout, func() {
		untrack()
		release()
	})
	if err := p.handshake(ctx); err != nil {
		p.stop(0)
		return nil, err
	}
	log.Debug("document core ready", "pid", cmd.Process.Pid, "confined", p.confined)
	return p, nil
}

func (p *process) handshake(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, startTimeout)
	defer cancel()
	frame, err := p.receive(ctx)
	if err != nil {
		return fmt.Errorf("document core handshake: %w", err)
	}
	var hello struct {
		Protocol int `json:"protocol"`
	}
	if err := json.Unmarshal(frame, &hello); err != nil || hello.Protocol != protocolVersion {
		return fmt.Errorf("document core speaks protocol %d, host requires %d", hello.Protocol, protocolVersion)
	}
	return nil
}

// read delivers frames until the process closes its output, then reaps it.
func (p *process) read(stdout io.Reader, release func()) {
	reader := bufio.NewReaderSize(stdout, 64<<10)
	var readErr error
	for {
		frame, err := readFrame(reader)
		if err != nil {
			readErr = err
			break
		}
		p.frames <- frame
	}
	waitErr := p.cmd.Wait()
	release()
	select {
	case <-p.stopping:
		p.exitErr = ErrExited
	default:
		exitErr := &ExitError{Status: exitStatus(p.cmd.ProcessState, waitErr, readErr), Diagnostics: p.stderr.String()}
		p.exitErr = exitErr
		log.Warn("document core ended unexpectedly", "status", exitErr.Status, "stderr", p.stderr.String())
	}
	close(p.done)
}

func (p *process) exchange(ctx context.Context, request []byte) ([]byte, error) {
	select {
	case <-p.done:
		return nil, p.exitErr
	default:
	}
	if err := writeFrame(p.stdin, request); err != nil {
		<-p.done
		return nil, p.exitErr
	}
	return p.receive(ctx)
}

func (p *process) receive(ctx context.Context) ([]byte, error) {
	select {
	case frame := <-p.frames:
		return frame, nil
	case <-p.done:
		// A frame read just before exit still answers its request.
		select {
		case frame := <-p.frames:
			return frame, nil
		default:
			return nil, p.exitErr
		}
	case <-ctx.Done():
		return nil, ErrTimeout
	}
}

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// stop ends the process: closing input asks it to exit, and a process that
// has not within grace is killed. A zero grace kills at once.
func (p *process) stop(grace time.Duration) {
	p.stopOnce.Do(func() {
		close(p.stopping)
		_ = p.stdin.Close()
		if grace > 0 {
			select {
			case <-p.done:
				return
			case <-time.After(grace):
			}
		}
		_ = p.cmd.Process.Kill()
	})
	<-p.done
}

func readFrame(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	length := binary.LittleEndian.Uint32(header[:])
	if length > maxRequestBytes {
		return nil, fmt.Errorf("document core frame of %d bytes exceeds the limit", length)
	}
	frame := make([]byte, length)
	if _, err := io.ReadFull(r, frame); err != nil {
		return nil, err
	}
	return frame, nil
}

func writeFrame(w io.Writer, frame []byte) error {
	if len(frame) > math.MaxUint32 {
		return fmt.Errorf("document core frame of %d bytes exceeds its length prefix", len(frame))
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(frame))) // #nosec G115 -- bounded above
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(frame)
	return err
}

func exitStatus(state *os.ProcessState, waitErr, readErr error) string {
	if state != nil {
		return state.String()
	}
	if waitErr != nil {
		return waitErr.Error()
	}
	return readErr.Error()
}

// tail keeps the last bytes written to it.
type tail struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (t *tail) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, b...)
	if extra := len(t.data) - t.limit; extra > 0 {
		t.data = append(t.data[:0], t.data[extra:]...)
	}
	return len(b), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.data))
}
