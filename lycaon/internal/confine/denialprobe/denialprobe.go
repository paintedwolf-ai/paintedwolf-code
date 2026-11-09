// Package denialprobe is the program a confined process runs to report what its boundary does.
// It answers with the kernel's result for each step and decides nothing; the caller reads the
// typed results. Any binary linking internal/confine can be this program.
package denialprobe

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"syscall"
)

// Flag selects the probe in a re-executed engine binary.
const Flag = "__confine-denial-probe"

// Kind names one operation the probe attempts.
type Kind string

const (
	Stat       Kind = "stat"
	ReadFile   Kind = "read_file"
	CreateFile Kind = "create_file"
	// OverwriteFile opens an existing file for writing and changes nothing in it.
	OverwriteFile Kind = "overwrite_file"
	Connect       Kind = "connect"
	Unmount       Kind = "unmount"
	Mount         Kind = "mount"
)

// Step is one attempted operation.
type Step struct {
	Kind Kind   `json:"kind"`
	Path string `json:"path,omitempty"`
	Port uint16 `json:"port,omitempty"`
}

// Result is the kernel's answer to one Step. Errno 0 means the operation succeeded.
type Result struct {
	Step
	Errno int `json:"errno"`
}

// unclassified marks a failure the kernel did not report as an errno.
const unclassified = -1

// Succeeded reports that the operation took effect.
func (r Result) Succeeded() bool { return r.Errno == 0 }

// Denied reports the answers a boundary gives. A masked path answers as absent or read-only, and
// anything else, such as an I/O error, says nothing about enforcement.
func (r Result) Denied() bool {
	switch syscall.Errno(r.Errno) { //nolint:gosec // an errno the kernel returned, or -1, which matches nothing.
	case syscall.EPERM, syscall.EACCES, syscall.ENOENT, syscall.EROFS:
		return r.Errno > 0
	default:
		return false
	}
}

// Invoked reports whether argv requests the probe.
func Invoked(argv []string) bool { return len(argv) == 3 && argv[1] == Flag }

// Encode renders the probe's argument for steps.
func Encode(steps []Step) (string, error) {
	raw, err := json.Marshal(steps)
	if err != nil {
		return "", fmt.Errorf("encode denial probe: %w", err)
	}
	return string(raw), nil
}

// Serve attempts each step in order, writing one JSON result per line as it goes. A process the
// boundary kills at a step leaves the results before it.
func Serve(arg string, out io.Writer) error {
	var steps []Step
	if err := json.Unmarshal([]byte(arg), &steps); err != nil {
		return fmt.Errorf("decode denial probe: %w", err)
	}
	encoder := json.NewEncoder(out)
	for _, step := range steps {
		if err := encoder.Encode(Result{Step: step, Errno: errnoOf(attempt(step))}); err != nil {
			return fmt.Errorf("write denial probe result: %w", err)
		}
	}
	return nil
}

func attempt(step Step) error {
	switch step.Kind {
	case Stat:
		_, err := os.Stat(step.Path)
		return err
	case ReadFile:
		file, err := os.Open(step.Path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		_, err = file.Read(make([]byte, 1))
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	case CreateFile:
		return createAndRemove(step.Path)
	case OverwriteFile:
		file, err := os.OpenFile(step.Path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		return file.Close()
	case Connect:
		conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(step.Port))))
		if err != nil {
			return err
		}
		return conn.Close()
	case Unmount:
		return unmount(step.Path)
	case Mount:
		return mount(step.Path)
	default:
		return fmt.Errorf("unknown denial probe step %q", step.Kind)
	}
}

// createAndRemove leaves nothing behind, so an attempt the boundary wrongly allows is reported
// without residue.
func createAndRemove(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write([]byte{0})
	return errors.Join(writeErr, file.Close(), os.Remove(path))
}

func errnoOf(err error) int {
	if err == nil {
		return 0
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return int(errno)
	}
	return unclassified
}
