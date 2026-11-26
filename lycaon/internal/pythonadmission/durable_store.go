package pythonadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
)

// DurableContinuationSnapshot is the persisted state of approval leases and
// host-restart settlements.
type DurableContinuationSnapshot struct {
	Leases      []ApprovalLease           `json:"leases"`
	Settlements []RestartSettlementReport `json:"settlements,omitempty"`
}

// DurableContinuationStore persists approval leases and restart settlements
// across host restarts.
type DurableContinuationStore interface {
	Load(ctx context.Context) (DurableContinuationSnapshot, error)
	SaveLease(ctx context.Context, lease ApprovalLease) error
	SaveRestartSettlement(ctx context.Context, report RestartSettlementReport, updatedLeases []ApprovalLease) error
}

// FileContinuationStore is a JSON-file-backed DurableContinuationStore that
// persists state atomically through internal/fseffect.Replace.
type FileContinuationStore struct {
	rootDir string
	relFile string
	mu      sync.Mutex
}

// NewFileContinuationStore creates a FileContinuationStore writing to
// filepath.Join(rootDir, relFile) via fseffect.Replace.
func NewFileContinuationStore(rootDir, relFile string) (*FileContinuationStore, error) {
	if rootDir == "" || relFile == "" {
		return nil, errors.New("file continuation store requires rootDir and relFile")
	}
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve continuation store root: %w", err)
	}
	return &FileContinuationStore{
		rootDir: absRoot,
		relFile: relFile,
	}, nil
}

// Load reads the persisted DurableContinuationSnapshot from disk. A missing
// file returns an empty snapshot with nil error.
func (s *FileContinuationStore) Load(_ context.Context) (DurableContinuationSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// SaveLease upserts lease into the durable snapshot and writes it atomically.
// Terminal states (consumed, lost_on_restart) are never regressed by a late
// write from a stale process handle.
func (s *FileContinuationStore) SaveLease(_ context.Context, lease ApprovalLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.loadLocked()
	if err != nil {
		return err
	}
	replaced := false
	for i := range snap.Leases {
		if snap.Leases[i].LeaseID == lease.LeaseID {
			if isTerminalLeaseState(snap.Leases[i].State) && snap.Leases[i].State != lease.State {
				return nil
			}
			snap.Leases[i] = lease
			replaced = true
			break
		}
	}
	if !replaced {
		snap.Leases = append(snap.Leases, lease)
	}
	return s.saveLocked(snap)
}

// SaveRestartSettlement atomically persists updated leases and appends the
// RestartSettlementReport to durable history.
func (s *FileContinuationStore) SaveRestartSettlement(_ context.Context, report RestartSettlementReport, updatedLeases []ApprovalLease) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, err := s.loadLocked()
	if err != nil {
		return err
	}
	byID := make(map[string]ApprovalLease, len(updatedLeases))
	for _, l := range updatedLeases {
		byID[l.LeaseID] = l
	}
	for i := range snap.Leases {
		if upd, ok := byID[snap.Leases[i].LeaseID]; ok {
			snap.Leases[i] = upd
			delete(byID, upd.LeaseID)
		}
	}
	for _, l := range updatedLeases {
		if _, remaining := byID[l.LeaseID]; remaining {
			snap.Leases = append(snap.Leases, l)
		}
	}
	snap.Settlements = append(snap.Settlements, report)
	return s.saveLocked(snap)
}

func (s *FileContinuationStore) loadLocked() (DurableContinuationSnapshot, error) {
	fullPath := filepath.Join(s.rootDir, s.relFile)
	raw, err := os.ReadFile(fullPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DurableContinuationSnapshot{}, nil
		}
		return DurableContinuationSnapshot{}, fmt.Errorf("read continuation store: %w", err)
	}
	var snap DurableContinuationSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return DurableContinuationSnapshot{}, fmt.Errorf("decode continuation store: %w", err)
	}
	return snap, nil
}

func (s *FileContinuationStore) saveLocked(snap DurableContinuationSnapshot) error {
	raw, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("encode continuation store: %w", err)
	}
	_, err = fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.Location{
			Root: s.rootDir,
			Rel:  s.relFile,
		},
		Source: bytes.NewReader(raw),
		Mode:   0o600,
	})
	if err != nil {
		return fmt.Errorf("persist continuation store: %w", err)
	}
	return nil
}

func isTerminalLeaseState(st LeaseState) bool {
	return st == LeaseConsumed || st == LeaseDeclined || st == LeaseCancelled || st == LeaseLostOnRestart
}
