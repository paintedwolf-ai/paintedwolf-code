package llm

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"math/big"
	"sync"
)

// PoolSelector picks models from agent_pool.
type PoolSelector struct {
	mu     sync.Mutex
	policy ModelPolicy
	cursor int
}

// NewPoolSelector constructs a selector for the given policy snapshot.
func NewPoolSelector(policy ModelPolicy) *PoolSelector {
	return &PoolSelector{policy: normalizePolicy(policy)}
}

// UpdatePolicy replaces the active policy.
func (s *PoolSelector) UpdatePolicy(policy ModelPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = normalizePolicy(policy)
}

// SelectOne picks one model for single-agent dispatch.
func (s *PoolSelector) SelectOne(ctx context.Context) (*ModelSelection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pool := s.policy.AgentPool.Models
	if len(pool) == 0 {
		return selectionFromRef(s.policy.Coordinator, ModelRoleCoordinator, false), nil
	}
	switch s.policy.AgentPool.Selection {
	case PoolSelectionFirst:
		return selectionFromRef(pool[0], ModelRolePool, false), nil
	case PoolSelectionRandom:
		idx, err := randInt(len(pool))
		if err != nil {
			return nil, err
		}
		return selectionFromRef(pool[idx], ModelRolePool, false), nil
	default: // round_robin
		idx := s.cursor % len(pool)
		s.cursor++
		return selectionFromRef(pool[idx], ModelRolePool, false), nil
	}
}

func selectionFromRef(ref ModelRef, role ModelRole, fallback bool) *ModelSelection {
	return &ModelSelection{
		ProviderID: ref.ProviderID,
		Model:      ref.Model,
		Role:       role,
		Fallback:   fallback,
	}
}

func randInt(max int) (int, error) {
	if max <= 1 {
		return 0, nil
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		var b [8]byte
		if _, err2 := rand.Read(b[:]); err2 != nil {
			return 0, err
		}
		return int(binary.BigEndian.Uint64(b[:]) % uint64(max)), nil
	}
	return int(n.Int64()), nil
}
