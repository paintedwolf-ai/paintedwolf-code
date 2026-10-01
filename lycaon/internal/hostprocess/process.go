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
)

var ErrStale = errors.New("process reference is stale or belongs to another task")
var ErrUnsupported = errors.New("instance-bound process operations are unavailable")

// Process omits arguments and environment to avoid exposing secrets.
type Process struct {
	Reference  string `json:"reference,omitempty"`
	PID        int    `json:"pid"`
	ParentPID  int    `json:"parent_pid"`
	UID        uint32 `json:"uid"`
	Name       string `json:"name"`
	Executable string `json:"executable,omitempty"`
	Instance   string `json:"instance"`
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
type Service struct{ key [32]byte }

func New() (*Service, error) {
	s := &Service{}
	if _, err := rand.Read(s.key[:]); err != nil {
		return nil, fmt.Errorf("process reference key: %w", err)
	}
	return s, nil
}

// List requires prior approval and returns a PID-ordered snapshot.
func (s *Service) List(ctx context.Context, session string, pid, after, limit int) (Snapshot, error) {
	pids, err := listPIDs()
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
		process.Reference, err = s.encode(session, process)
		if err != nil {
			return Snapshot{Unavailable: unavailable}, err
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
	current.Reference = token
	return current, nil
}

func sameInstance(a, b Process) bool {
	return a.PID == b.PID && a.Instance == b.Instance && a.UID == b.UID && a.Executable == b.Executable
}

// Signal revalidates review evidence and uses an atomic instance-targeted kernel call.
func (s *Service) Signal(ctx context.Context, process Process, signal string) error {
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
	return signalInstance(process, signal)
}
