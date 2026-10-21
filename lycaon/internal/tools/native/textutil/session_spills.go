package textutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/tools"
)

// spillRootName is the private directory under the session TMPDIR that holds
// every program's sort runs; no program operand names it.
const spillRootName = ".painted-wolf-sort-spills"

// SessionSpills holds one program's sort runs in a private directory under
// its session's TMPDIR. Byte quotas stay with the program's SpillAdmission;
// this owner only places and removes runs. Close removes the program's
// directory when the program settles; ReclaimSessionSpills removes every
// program's leftovers for a session after host loss.
type SessionSpills struct {
	mu     sync.Mutex
	dir    string
	closed bool
}

var _ SpillStorage = (*SessionSpills)(nil)

// NewSessionSpills is the program runtime's one-line wiring: one value per
// program, closed when the program settles.
func NewSessionSpills(tc tools.ToolContext, programID string) (*SessionSpills, error) {
	scratch := strings.TrimSpace(tc.Host.SessionScratchDir)
	if scratch == "" {
		return nil, tools.ErrSessionScratchUnavailable
	}
	if programID == "" || filepath.Base(programID) != programID || strings.HasPrefix(programID, ".") {
		return nil, errors.New("sort spills require a program identity")
	}
	return &SessionSpills{dir: filepath.Join(scratch, spillRootName, programID)}, nil
}

// Create opens a new run, creating the program's directory on first use.
func (s *SessionSpills) Create(ctx context.Context) (SpillRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("program sort spills are closed")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	return DirectorySpills{Dir: s.dir}.Create(ctx)
}

// Close removes every run of the program, including runs an interrupted
// operation left behind, and refuses later runs.
func (s *SessionSpills) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return os.RemoveAll(s.dir)
}

// ReclaimSessionSpills removes all programs' sort runs for a session. The
// host calls it after supervisor loss, when no program of the session runs.
func ReclaimSessionSpills(tc tools.ToolContext) error {
	scratch := strings.TrimSpace(tc.Host.SessionScratchDir)
	if scratch == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(scratch, spillRootName))
}
