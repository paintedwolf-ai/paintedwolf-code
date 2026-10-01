package llm

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
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

// SelectGroup picks n models maximizing diversity for group dispatch.
func (s *PoolSelector) SelectGroup(ctx context.Context, n int) ([]ModelSelection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n <= 0 {
		return nil, fmt.Errorf("select group: n must be positive")
	}
	s.mu.Lock()
	pool := append([]ModelRef(nil), s.policy.AgentPool.Models...)
	coord := s.policy.Coordinator
	s.mu.Unlock()

	if len(pool) == 0 {
		out := make([]ModelSelection, n)
		sel := selectionFromRef(coord, ModelRoleCoordinator, false)
		for i := range out {
			out[i] = *sel
		}
		return out, nil
	}

	picked := diversePick(pool, n)
	out := make([]ModelSelection, len(picked))
	for i, ref := range picked {
		out[i] = *selectionFromRef(ref, ModelRolePool, false)
	}
	return out, nil
}

func selectionFromRef(ref ModelRef, role ModelRole, fallback bool) *ModelSelection {
	return &ModelSelection{
		ProviderID: ref.ProviderID,
		Model:      ref.Model,
		Role:       role,
		Fallback:   fallback,
	}
}

func poolKey(ref ModelRef) string {
	return ref.ProviderID + "\x00" + ref.Model
}

func providerKey(ref ModelRef) string {
	return ref.ProviderID
}

// diversePick greedily maximizes distinct provider_id then model pairs.
func diversePick(pool []ModelRef, n int) []ModelRef {
	if n <= 0 || len(pool) == 0 {
		return nil
	}
	remaining := append([]ModelRef(nil), pool...)
	var out []ModelRef
	usedProviders := make(map[string]struct{})
	usedKeys := make(map[string]struct{})

	for len(out) < n && len(remaining) > 0 {
		bestIdx := -1
		bestScore := -1
		for i, ref := range remaining {
			score := 0
			pk := providerKey(ref)
			k := poolKey(ref)
			if _, ok := usedProviders[pk]; !ok {
				score += 2
			}
			if _, ok := usedKeys[k]; !ok {
				score += 1
			}
			if score > bestScore {
				bestScore = score
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break
		}
		ref := remaining[bestIdx]
		out = append(out, ref)
		usedProviders[providerKey(ref)] = struct{}{}
		usedKeys[poolKey(ref)] = struct{}{}
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}

	for len(out) < n {
		out = append(out, out[len(out)%len(pool)])
	}
	return out
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
