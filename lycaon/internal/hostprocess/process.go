// Package hostprocess owns host process snapshots and instance-bound signaling.
package hostprocess

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/lineage"
)

var ErrStale = errors.New("process reference is stale or belongs to another session")
var ErrUnsupported = errors.New("instance-bound process operations are unavailable")

// ErrNotOwned reports a process that no launch of the session started. Approval
// cannot change it: the session's process authority is its own descendants.
var ErrNotOwned = errors.New("process was not started by this session")

// Process omits arguments and environment to avoid exposing secrets.
type Process struct {
	Reference  string `json:"reference,omitempty"`
	PID        int    `json:"pid"`
	ParentPID  int    `json:"parent_pid"`
	UID        uint32 `json:"uid"`
	Name       string `json:"name"`
	Executable string `json:"executable,omitempty"`
	Instance   string `json:"instance"`
	// Owned reports descent from a launch this session made. Only an owned
	// process carries a reference, so only an owned process can be signaled.
	Owned bool `json:"owned"`
	// Listeners holds the sockets a listener query matched for this process.
	Listeners  []ListeningSocket `json:"listeners,omitempty"`
	StartOrder uint64            `json:"-"`
	Stats      ProcessStats      `json:"-"`
}

// ProcessStats holds observed kernel counters for the read-only ps view.
// Individual fields remain empty when that kernel observation was unavailable.
type ProcessStats struct {
	CPUTime       time.Duration
	Started       time.Time
	VirtualBytes  uint64
	ResidentBytes uint64
	TTY           string
	State         string
	Available     bool
}

// VisibleProcess is a read-only process observation for shell ps and pgrep.
type VisibleProcess struct {
	Process
	Command string
}

// Observe reads the kernel identity of a live process. Callers must retain the
// instance, rather than treating a PID alone as ownership evidence.
func Observe(pid int) (Process, error) { return inspect(pid) }

// IsCurrent compares an observation with the current kernel instance.
func IsCurrent(process Process) bool {
	current, err := inspect(process.PID)
	return err == nil && sameInstance(current, process)
}

// VisibleProcesses lists processes visible to the current user in PID order.
func VisibleProcesses(ctx context.Context) ([]VisibleProcess, error) {
	pids, err := listPIDs()
	if err != nil {
		return nil, err
	}
	sort.Ints(pids)
	out := make([]VisibleProcess, 0, len(pids))
	for _, candidate := range pids {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if candidate <= 0 {
			continue
		}
		p, err := inspect(candidate)
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(readCommandLine(candidate))
		if cmd == "" {
			cmd = strings.TrimSpace(p.Executable)
		}
		if cmd == "" {
			cmd = strings.TrimSpace(p.Name)
		}
		current, err := inspect(candidate)
		if err != nil || !sameInstance(p, current) {
			continue
		}
		out = append(out, VisibleProcess{Process: p, Command: cmd})
	}
	return out, nil
}

// ProcessExists reports whether pid currently exists on the host.
func ProcessExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	if _, err := inspect(pid); err == nil {
		return true
	}
	return processExistsFallback(pid)
}

type Snapshot struct {
	Processes    []Process
	NextAfterPID int
	Unavailable  int
}

type reference struct {
	Process Process `json:"process"`
	Session string  `json:"session"`
}

// Service references expire on host restart and do not hold process descriptors.
type Service struct {
	key [32]byte
	// members reports the processes descended from the session's launches.
	members func(session string) (map[int]struct{}, error)
	// listeners reads the host's listening sockets and their holders.
	listeners func(lineage.ListenerQuery) ([]lineage.Listener, error)
}

func New() (*Service, error) {
	s := &Service{members: lineage.SessionMembers, listeners: lineage.Listeners}
	if _, err := rand.Read(s.key[:]); err != nil {
		return nil, fmt.Errorf("process reference key: %w", err)
	}
	return s, nil
}

// List requires prior approval and returns a PID-ordered snapshot. Every
// visible process is named; only the session's own carry a reference.
func (s *Service) List(ctx context.Context, session string, pid, after, limit int) (Snapshot, error) {
	pids, err := listPIDs()
	if err != nil {
		return Snapshot{}, err
	}
	owned, err := s.members(session)
	if err != nil {
		return Snapshot{}, err
	}
	sort.Ints(pids)
	out := make([]Process, 0, limit)
	unavailable, next := 0, 0
	for _, candidate := range pids {
		if err := ctx.Err(); err != nil {
			return Snapshot{Unavailable: unavailable}, err
		}
		if candidate <= after || candidate <= 0 || (pid > 0 && candidate != pid) {
			continue
		}
		if len(out) == limit {
			next = out[len(out)-1].PID
			break
		}
		process, err := inspect(candidate)
		if err != nil {
			unavailable++
			continue
		}
		if _, process.Owned = owned[candidate]; process.Owned {
			process.Reference, err = s.encode(session, process)
			if err != nil {
				return Snapshot{Unavailable: unavailable}, err
			}
		}
		out = append(out, process)
	}
	return Snapshot{Processes: out, NextAfterPID: next, Unavailable: unavailable}, nil
}

func (s *Service) encode(session string, process Process) (string, error) {
	raw, err := json.Marshal(reference{Process: process, Session: session})
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, s.key[:])
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Resolve validates both host provenance and current kernel identity before review.
func (s *Service) Resolve(session, token string) (Process, error) {
	if len(token) > 16384 {
		return Process{}, ErrStale
	}
	payload, signature, ok := strings.Cut(token, ".")
	if !ok {
		return Process{}, ErrStale
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Process{}, ErrStale
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return Process{}, ErrStale
	}
	mac := hmac.New(sha256.New, s.key[:])
	_, _ = mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return Process{}, ErrStale
	}
	var ref reference
	if json.Unmarshal(raw, &ref) != nil || ref.Session != session || ref.Process.PID <= 0 {
		return Process{}, ErrStale
	}
	current, err := inspect(ref.Process.PID)
	if err != nil {
		return Process{}, err
	}
	if !sameInstance(current, ref.Process) {
		return Process{}, ErrStale
	}
	if err := s.owns(session, current.PID); err != nil {
		return Process{}, err
	}
	current.Reference, current.Owned = token, true
	return current, nil
}

func (s *Service) owns(session string, pid int) error {
	owned, err := s.members(session)
	if err != nil {
		return err
	}
	if _, ok := owned[pid]; !ok {
		return ErrNotOwned
	}
	return nil
}

func sameInstance(a, b Process) bool {
	return a.PID == b.PID && a.Instance == b.Instance && a.UID == b.UID && a.Executable == b.Executable
}

// Signal revalidates review evidence and session ownership, then uses an
// atomic instance-targeted kernel call.
func (s *Service) Signal(ctx context.Context, session string, process Process, signal string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := inspect(process.PID)
	if err != nil {
		return err
	}
	if !sameInstance(current, process) {
		return ErrStale
	}
	if err := s.owns(session, process.PID); err != nil {
		return err
	}
	kind, err := parseSignal(signal)
	if err != nil {
		return err
	}
	return signalInstance(process, kind)
}
