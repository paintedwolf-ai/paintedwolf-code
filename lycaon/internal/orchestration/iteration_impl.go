package orchestration

import (
	"context"
	"fmt"
	"sync"
)

type iterationKey struct {
	agentID string
	taskID  string
}

// InMemoryIterationCap tracks iterations and stuck detection per agent/task in memory.
type InMemoryIterationCap struct {
	mu     sync.Mutex
	max    int
	states map[iterationKey]IterationState
}

// NewInMemoryIterationCap constructs a cap tracker with the given max (defaults to MaxIterations).
func NewInMemoryIterationCap(max int) *InMemoryIterationCap {
	if max <= 0 {
		max = MaxIterations
	}
	return &InMemoryIterationCap{
		max:    max,
		states: make(map[iterationKey]IterationState),
	}
}

// Track increments the iteration count for agentID/taskID.
func (c *InMemoryIterationCap) Track(ctx context.Context, agentID, taskID string) (IterationState, error) {
	if err := ctx.Err(); err != nil {
		return IterationState{}, err
	}
	if c == nil {
		return IterationState{}, fmt.Errorf("iteration cap not configured")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := iterationKey{agentID: agentID, taskID: taskID}
	st := c.states[k]
	st.Count++
	c.states[k] = st
	return st, nil
}

// NoteError records an iteration error for stuck detection (3 consecutive identical errors).
func (c *InMemoryIterationCap) NoteError(_ context.Context, agentID, taskID string, iterationErr error) (IterationState, error) {
	if c == nil {
		return IterationState{}, fmt.Errorf("iteration cap not configured")
	}
	if iterationErr == nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.states[iterationKey{agentID: agentID, taskID: taskID}], nil
	}
	msg := iterationErr.Error()
	c.mu.Lock()
	defer c.mu.Unlock()
	k := iterationKey{agentID: agentID, taskID: taskID}
	st := c.states[k]
	if st.LastError != nil && *st.LastError == msg {
		st.ErrorCount++
	} else {
		st.ErrorCount = 1
		copyMsg := msg
		st.LastError = &copyMsg
	}
	st.Stuck = st.ErrorCount >= MaxStuckIterations
	c.states[k] = st
	return st, nil
}

// Check evaluates termination using the tracker's default max.
func (c *InMemoryIterationCap) Check(ctx context.Context, agentID, taskID string) (*TerminationDecision, error) {
	return c.CheckMax(ctx, agentID, taskID, c.max)
}

// CheckMax evaluates termination against an explicit max (topology override).
func (c *InMemoryIterationCap) CheckMax(ctx context.Context, agentID, taskID string, max int) (*TerminationDecision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil {
		return nil, fmt.Errorf("iteration cap not configured")
	}
	if max <= 0 {
		max = MaxIterations
	}
	c.mu.Lock()
	st := c.states[iterationKey{agentID: agentID, taskID: taskID}]
	c.mu.Unlock()

	if st.Stuck {
		return &TerminationDecision{
			ShouldTerminate: true,
			Reason:          TerminationReasonStuck,
			NextAction:      NextActionReassign,
		}, nil
	}
	if st.Count > max {
		return &TerminationDecision{
			ShouldTerminate: true,
			Reason:          TerminationReasonMaxIterations,
			NextAction:      NextActionAbort,
		}, nil
	}
	return nil, nil
}

// DefaultSelfTermination exits early on high confidence or stable output.
type DefaultSelfTermination struct {
	ConfidenceThreshold float64
}

// NewDefaultSelfTermination constructs a self-termination evaluator.
func NewDefaultSelfTermination() *DefaultSelfTermination {
	return &DefaultSelfTermination{ConfidenceThreshold: DefaultConvergenceConfidence}
}

// Evaluate returns a convergence termination decision when thresholds are met.
func (s *DefaultSelfTermination) Evaluate(ctx context.Context, state TerminationContext) (*TerminationDecision, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("self-termination not configured")
	}
	threshold := s.ConfidenceThreshold
	if threshold <= 0 {
		threshold = DefaultConvergenceConfidence
	}
	if state.OutputStable || state.Confidence >= threshold {
		return &TerminationDecision{
			ShouldTerminate: true,
			Reason:          TerminationReasonConvergence,
			NextAction:      NextActionAbort,
		}, nil
	}
	return nil, nil
}

// EffectiveIterationCap returns the cap for a topology spec (YAML override or default).
func EffectiveIterationCap(spec TopologySpec) int {
	if spec.IterationCap > 0 {
		return spec.IterationCap
	}
	return DefaultIterationCap
}
